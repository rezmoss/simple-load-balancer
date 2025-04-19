package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// Backend represents a backend server
type Backend struct {
	URL          *url.URL
	Alive        bool
	mux          sync.RWMutex
	ReverseProxy *httputil.ReverseProxy
}

// SetAlive updates the alive status of backend
func (b *Backend) SetAlive(alive bool) {
	b.mux.Lock()
	b.Alive = alive
	b.mux.Unlock()
}

// IsAlive returns true when backend is alive
func (b *Backend) IsAlive() (alive bool) {
	b.mux.RLock()
	alive = b.Alive
	b.mux.RUnlock()
	return
}

// LoadBalancer represents a load balancer
type LoadBalancer struct {
	backends []*Backend
	current  uint64
}

// NextBackend returns the next available backend to handle the request
func (lb *LoadBalancer) NextBackend() *Backend {
	// Simple round-robin
	next := atomic.AddUint64(&lb.current, uint64(1)) % uint64(len(lb.backends))

	// Find the next available backend
	for i := 0; i < len(lb.backends); i++ {
		idx := (int(next) + i) % len(lb.backends)
		if lb.backends[idx].IsAlive() {
			return lb.backends[idx]
		}
	}
	return nil
}

// isBackendAlive checks whether a backend is alive by establishing a TCP connection
func isBackendAlive(u *url.URL) bool {
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		log.Printf("Site unreachable: %s", err)
		return false
	}
	defer conn.Close()
	return true
}

// HealthCheck pings the backends and updates their status
func (lb *LoadBalancer) HealthCheck() {
	for _, b := range lb.backends {
		status := isBackendAlive(b.URL)
		b.SetAlive(status)
		if status {
			log.Printf("Backend %s is alive", b.URL)
		} else {
			log.Printf("Backend %s is dead", b.URL)
		}
	}
}

// HealthCheckPeriodically runs a routine health check every interval
func (lb *LoadBalancer) HealthCheckPeriodically(interval time.Duration) {
	t := time.NewTicker(interval)
	for {
		select {
		case <-t.C:
			lb.HealthCheck()
		}
	}
}

// ServeHTTP implements the http.Handler interface for the LoadBalancer
func (lb *LoadBalancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	backend := lb.NextBackend()
	if backend == nil {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}

	// Log the request
	log.Printf("Routing request %s %s to backend %s", r.Method, r.URL.Path, backend.URL)

	// Forward the request to the backend
	backend.ReverseProxy.ServeHTTP(w, r)
}

func main() {
	// Parse command line flags
	port := flag.Int("port", 8080, "Port to serve on")
	checkInterval := flag.Duration("check-interval", time.Minute, "Interval for health checking backends")
	flag.Parse()

	// Configure backends (in a real application, this might come from a config file)
	serverList := []string{
		"http://localhost:8081",
		"http://localhost:8082",
		"http://localhost:8083",
	}

	// Create load balancer
	lb := LoadBalancer{}

	// Initialize backends
	for _, serverURL := range serverList {
		url, err := url.Parse(serverURL)
		if err != nil {
			log.Fatal(err)
		}

		proxy := httputil.NewSingleHostReverseProxy(url)

		// Customize the reverse proxy director
		originalDirector := proxy.Director
		proxy.Director = func(r *http.Request) {
			originalDirector(r)
			r.Header.Set("X-Proxy", "Simple-Load-Balancer")
		}

		// Add custom error handler
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("Proxy error: %v", err)
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)

			// Mark the backend as down
			for _, b := range lb.backends {
				if b.URL.String() == url.String() {
					b.SetAlive(false)
					break
				}
			}
		}

		lb.backends = append(lb.backends, &Backend{
			URL:          url,
			Alive:        true,
			ReverseProxy: proxy,
		})
		log.Printf("Configured backend: %s", url)
	}

	// Initial health check
	lb.HealthCheck()

	// Start periodic health check
	go lb.HealthCheckPeriodically(*checkInterval)

	// Set up graceful shutdown signal handler
	// Note: In a production environment, you would implement proper
	// signal handling for graceful shutdown

	// Start the server
	server := http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: &lb,
		// Set reasonable timeouts
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("Load Balancer started at :%d\n", *port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
