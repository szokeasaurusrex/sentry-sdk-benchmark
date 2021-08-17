package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	cadvisor "github.com/google/cadvisor/client/v2"
	cadvisor_info "github.com/google/cadvisor/info/v2"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

const target = "http://app:8080"

func main() {
	log.Print("Load Generator")
	defer log.Print("Bye!")
	defer func() {
		if err := recover(); err != nil {
			log.Fatal(err)
		}
	}()

	path := os.Getenv("RESULT_PATH")
	if path == "" {
		panic("Missing RESULT_PATH env var, aborting!")
	}

	containerName := os.Getenv("TARGET_CONTAINER_NAME")
	if containerName == "" {
		panic("Missing TARGET_CONTAINER_NAME env var, aborting!")
	}

	waitUntilReady()
	warmUp()

	result := test(containerName)

	save(result, path)
}

// waitUntilReady waits until the target web app is ready to receive traffic.
func waitUntilReady() {
	log.Print("Waiting until web app is ready")
	ready := false
	for i := 0; i < 5; i++ {
		ready = fetch(target+"/update?query=1", 5*time.Second).Success == 1
		if ready {
			break
		}
		log.Print("Web app not ready, waiting...")
		time.Sleep(2 * time.Second)
	}
	if !ready {
		panic("Web app not ready")
	}
}

// warmUp sends some traffic to warm up the target web app, ensuring
// connectivity with the database is established, caches are warm, any JIT has
// taken place, etc.
func warmUp() {
	log.Print("Warming up web app")
	fetch(target+"/update?query=100", 15*time.Second)
}

type TestResult struct {
	*vegeta.Metrics
	Stats map[string]Stats `json:"container_stats"`
}

type Stats struct {
	Before     ContainerStats           `json:"before"`
	After      ContainerStats           `json:"after"`
	Difference ContainerStatsDifference `json:"difference"`
}

type ContainerStats struct {
	Timestamp           time.Time `json:"timestamp"`
	MemoryMaxUsageBytes uint64    `json:"memory_max_usage_bytes"`
	CPUUsageUser        uint64    `json:"cpu_usage_user"`
	CPUUsageSystem      uint64    `json:"cpu_usage_system"`
	CPUUsageTotal       uint64    `json:"cpu_usage_total"`
}

type ContainerStatsDifference struct {
	Duration            time.Duration `json:"duration"`
	MemoryMaxUsageBytes int64         `json:"memory_max_usage_bytes"`
	CPUUsageUser        int64         `json:"cpu_usage_user"`
	CPUUsageSystem      int64         `json:"cpu_usage_system"`
	CPUUsageTotal       int64         `json:"cpu_usage_total"`
}

// test sends the actual test traffic to the target web app and returns the
// collected results.
func test(containerName string) TestResult {
	log.Print("Testing web app")

	stats := Stats{}
	stats.Before = containerStats(containerName)

	metrics := fetch(target+"/update?query=100", 20*time.Second)

	stats.After = containerStats(containerName)
	// Note: potential overflow ignored for simplicity
	stats.Difference.Duration = stats.After.Timestamp.Sub(stats.Before.Timestamp)
	stats.Difference.MemoryMaxUsageBytes = int64(stats.After.MemoryMaxUsageBytes - stats.Before.MemoryMaxUsageBytes)
	stats.Difference.CPUUsageUser = int64(stats.After.CPUUsageUser - stats.Before.CPUUsageUser)
	stats.Difference.CPUUsageSystem = int64(stats.After.CPUUsageSystem - stats.Before.CPUUsageSystem)
	stats.Difference.CPUUsageTotal = int64(stats.After.CPUUsageTotal - stats.Before.CPUUsageTotal)

	return TestResult{
		Metrics: metrics,
		Stats:   map[string]Stats{"app": stats},
	}
}

// fetch requests the given URL several times for the given duration and with
// the given concurrency level.
func fetch(url string, duration time.Duration) *vegeta.Metrics {
	target := vegeta.NewStaticTargeter(vegeta.Target{
		Method: "GET",
		URL:    url,
	})
	rate := vegeta.Rate{Freq: 100, Per: time.Second}
	attacker := vegeta.NewAttacker()
	ch := attacker.Attack(target, rate, duration, "")

	var m vegeta.Metrics
	for res := range ch {
		m.Add(res)
	}
	m.Close()
	return &m
}

func containerStats(containerName string) ContainerStats {
	client, err := cadvisor.NewClient("http://cadvisor:8080/")
	if err != nil {
		panic(err)
	}
	opts := &cadvisor_info.RequestOptions{
		IdType: cadvisor_info.TypeDocker,
		Count:  1,
	}
	m, err := client.Stats(containerName, opts)
	if err != nil {
		panic(err)
	}
	for _, v := range m {
		return ContainerStats{
			Timestamp:           v.Stats[0].Timestamp,
			MemoryMaxUsageBytes: v.Stats[0].Memory.MaxUsage,
			CPUUsageUser:        v.Stats[0].Cpu.Usage.User,
			CPUUsageSystem:      v.Stats[0].Cpu.Usage.System,
			CPUUsageTotal:       v.Stats[0].Cpu.Usage.Total,
		}
	}
	panic("missing cAdvisor stats")
}

// save writes reports computed from metrics to the output path.
func save(r TestResult, path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
		panic(err)
	}
	writeReport(vegeta.NewTextReporter(r.Metrics), path+".txt")
	writeReport(NewJSONReporter(r), path+".json")
	writeReport(vegeta.NewHDRHistogramPlotReporter(r.Metrics), path+".hdr")
}

// writeReport writes the output of the reporter to the output path.
func writeReport(r vegeta.Reporter, path string) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := r.Report(f); err != nil {
		panic(err)
	}
}

// NewJSONReporter returns a vegeta.Reporter that writes out pretty JSON.
func NewJSONReporter(m interface{}) vegeta.Reporter {
	return func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(m)
	}
}