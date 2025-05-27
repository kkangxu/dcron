package dcron

import (
	"fmt"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func newEtcdDcron() Dcron {
	// Initialize Etcd client
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{"localhost:2379"},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("Failed to connect Etcd: %v", err)
	}

	// Create Etcd registry
	registry := NewEtcdRegistry(cli)

	// Create distributed cron scheduler
	dc := NewDcron(
		registry,
		WithStrategy(StrategyConsistent), // Use consistent hashing strategy
		WithCronOptions(cron.WithSeconds()),
		WithTaskRunFunc(func(task *TaskMeta) error {
			log.Printf("Executing dynamic task: %s with payload: %s", task.Name, task.Payload)
			return nil
		}),
	)
	return dc
}

func Test_DcronAddFunc(t *testing.T) {
	dc := newEtcdDcron()

	// Wait group to synchronize goroutines
	var wg sync.WaitGroup

	// Function to add tasks asynchronously
	addTasks := func() {
		defer wg.Done()
		for i := 1; i <= 5; i++ {
			taskName := "async-task-" + fmt.Sprintf("%d", i)
			err := dc.AddFunc(taskName, "*/5 * * * * *", func() error {
				t.Logf("Executing %s", taskName)
				return nil
			})
			if err != nil {
				t.Logf("Failed to add task %s: %v", taskName, err)
			}
		}
	}

	// Function to delete tasks asynchronously
	deleteTasks := func() {
		defer wg.Done()
		time.Sleep(10 * time.Second) // Wait for tasks to execute
		for i := 1; i <= 5; i++ {
			time.Sleep(1 * time.Second)
			taskName := "async-task-" + fmt.Sprintf("%d", i)
			err := dc.MarkTaskDeleted(taskName)
			if err != nil {
				t.Logf("Failed to delete task %s: %v", taskName, err)
			} else {
				t.Logf("Deleted task %s", taskName)
			}
		}
	}

	// Start adding tasks asynchronously
	wg.Add(1)
	go addTasks()

	// Start deleting tasks asynchronously
	wg.Add(1)
	go deleteTasks()

	// Start the Dcron service in a separate goroutine
	stop := make(chan struct{})
	go func() {
		if err := dc.Start(); err != nil {
			t.Fatalf("Failed to start Dcron: %v", err)
		}
		stop <- struct{}{}
	}()

	// Wait for all goroutines to finish
	wg.Wait()

	// Stop the Dcron service
	dc.Stop()
	<-stop
}

func Test_ForceAddFunc(t *testing.T) {
	dc := newEtcdDcron()

	// Wait group to synchronize goroutines
	var wg sync.WaitGroup

	// Function to add tasks asynchronously
	addTasks := func() {
		defer wg.Done()
		for i := 1; i <= 5; i++ {
			taskName := "async-task-" + fmt.Sprintf("%d", i)
			err := dc.AddFunc(taskName, "*/5 * * * * *", func() error {
				t.Logf("Executing %s", taskName)
				return nil
			})
			if err != nil {
				t.Logf("Failed to add task %s: %v", taskName, err)
			}
		}
	}

	// Function to forcefully overwrite tasks asynchronously
	forceOverwriteTasks := func() {
		defer wg.Done()
		time.Sleep(15 * time.Second) // Wait for tasks to execute
		for i := 1; i <= 5; i++ {
			taskName := "async-task-" + fmt.Sprintf("%d", i)
			err := dc.ForceAddFunc(taskName, "*/3 * * * * *", func() error {
				t.Logf("Forcefully executing %s", taskName)
				return nil
			})
			if err != nil {
				t.Logf("Failed to overwrite task %s: %v", taskName, err)
			}
		}
	}

	// Function to delete tasks asynchronously
	cleanupTask := func() {
		defer wg.Done()
		time.Sleep(30 * time.Second) // Wait for tasks to execute
		for i := 1; i <= 5; i++ {
			time.Sleep(1 * time.Second)
			taskName := "async-task-" + fmt.Sprintf("%d", i)
			err := dc.CleanupTask(taskName)
			if err != nil {
				t.Logf("Failed to delete task %s: %v", taskName, err)
			} else {
				t.Logf("Deleted task %s", taskName)
			}
		}
	}

	// Start adding tasks asynchronously
	wg.Add(1)
	go addTasks()

	// Start forcefully overwriting tasks after a delay
	wg.Add(1)
	go forceOverwriteTasks()

	// Start deleting tasks asynchronously
	wg.Add(1)
	go cleanupTask()

	// Start the Dcron service in a separate goroutine
	stop := make(chan struct{})
	go func() {
		if err := dc.Start(); err != nil {
			t.Fatalf("Failed to start Dcron: %v", err)
		}
		stop <- struct{}{}
	}()

	// Wait for all goroutines to finish
	wg.Wait()

	// wait for tasks to rebalanced
	time.Sleep(10 * time.Second)

	// Stop the Dcron service
	dc.Stop()
	<-stop
}
