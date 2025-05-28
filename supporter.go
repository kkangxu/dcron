package dcron

import (
	"fmt"
	"math/rand"
	"net"
	"time"
)

const (
	localIP = "127.0.0.1"
)

// getLocalIP returns the first non-loopback IPv4 address of the local machine.
// If no such address is found, it returns 127.0.0.1.
// This is used to identify the node in the cluster.
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return localIP
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return localIP
}

// generateNodeID generates a unique node ID based on IP, timestamp, and a random number.
// This ensures that each node has a globally unique identifier even if multiple nodes
// are started on the same machine at nearly the same time.
func generateNodeID(ip string) string {
	return fmt.Sprintf("%s-%d-%04d", ip, time.Now().UnixNano(), rand.Intn(1000))
}
