package dcron

import (
	"hash/crc32"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

func (dc *dcron) prepareWork() {
	dc.reloadNodes()
	dc.reloadTasks()
	dc.reloadDeletedTasks()
}

func (dc *dcron) asyncWork() {
	go dc.heartbeat()
	go dc.watchNodeEvent()
	go dc.watchTaskEvent()
	go dc.watchDeletedTaskEvent()
	go dc.rebalanced()
}

// heartbeat periodically updates the node's heartbeat and cleans up expired nodes
// It runs as a background goroutine to maintain node health in the registry
func (dc *dcron) heartbeat() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:

			if err := dc.registry.UpdateHeartbeat(dc.ctx, dc.nodeID); err != nil {
				logger.Errorf("Node heartbeat update failed in registry - node may be marked as expired: %v", err)
			}

			if err := dc.registry.CleanupExpiredNodes(dc.ctx, nodeTimeout); err != nil {
				logger.Errorf("Failed to clean up expired nodes from registry - stale nodes may remain active: %v", err)
			}

		case <-dc.ctx.Done():
			return
		}
	}
}

func (dc *dcron) watchNodeEvent() {
	ticker := time.NewTicker(reloadNodesPeriod)
	defer ticker.Stop()

	eventChan, err := dc.registry.WatchNodes(dc.ctx)
	if err != nil {
		logger.Errorf("Node status watch operation failed - unable to detect node membership changes: %v", err)
		return
	}

	for {
		select {
		case <-eventChan:
			if dc.reloadNodes() {
				dc.rebalancedChan <- struct{}{}
			}
		case <-ticker.C:
			if dc.reloadNodes() {
				dc.rebalancedChan <- struct{}{}
			}
		case <-dc.ctx.Done():
			return
		}
	}
}

func (dc *dcron) reloadNodes() bool {

	nodes, err := dc.registry.GetWorkingNodes(dc.ctx)
	if err != nil {
		logger.Errorf("Failed to retrieve working nodes from registry - task assignment may be using outdated node list: %v", err)
		return false
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	var newHash uint32
	{
		var nodeIDs []string
		for _, n := range nodes {
			nodeIDs = append(nodeIDs, n.ID)
		}

		sort.Strings(nodeIDs)
		newHash = crc32.ChecksumIEEE([]byte(strings.Join(nodeIDs, ",")))
	}

	dc.tasksRWMux.Lock()
	defer dc.tasksRWMux.Unlock()

	var oldHash uint32
	{
		var nodeIDs []string
		for _, n := range dc.nodes {
			nodeIDs = append(nodeIDs, n.ID)
		}
		sort.Strings(nodeIDs)

		oldHash = crc32.ChecksumIEEE([]byte(strings.Join(nodeIDs, ",")))
	}

	if newHash == oldHash {
		return false
	}
	dc.nodes = nodes
	return true
}

// waitForShutdown waits for a shutdown signal and gracefully stops the dcron instance
// It handles both OS signals (SIGINT, SIGTERM) and programmatic shutdown requests
func (dc *dcron) waitForShutdown() {

	// received os signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigChan:
		logger.Info("Received OS signal, initiating graceful shutdown process...")
	case <-dc.shutdownChan:
		logger.Info("Received API stop signal, initiating graceful shutdown process...")
	}

	// update node status to leaving
	if err := dc.registry.UpdateStatus(dc.ctx, dc.nodeID, NodeStatusLeaving); err != nil {
		logger.Errorf("Failed to update node status to 'leaving' before shutdown - node may remain in incorrect state: %v", err)
	}

	// stop cron scheduler
	ctx := dc.cr.Stop()
	<-ctx.Done()

	// unregister node
	if err := dc.registry.Unregister(dc.ctx, dc.nodeID); err != nil {
		logger.Errorf("Node unregistration failed during shutdown - resource cleanup may be incomplete: %v", err)
	}

	// cancel context
	dc.cancel()

	logger.Info("Dcron service has been successfully stopped and all resources released")
}
