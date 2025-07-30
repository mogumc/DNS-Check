package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

func PingNode(HostIP string) (time.Duration, error) {
	dialer := &net.Dialer{
		Timeout: 3 * time.Second,
	}
	start := time.Now()
	conn, err := dialer.Dial("tcp", HostIP+":53")
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	return time.Since(start), nil
}

type DNSResult struct {
	IP             string
	QueryLatency   time.Duration
	NetworkLatency time.Duration
	Score          float64
}

func resolveDNS(ip, domain string) (time.Duration, error) {
	client := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return net.Dial(network, ip+":53")
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	ips, err := client.LookupHost(ctx, domain)
	if err != nil {
		return 0, err
	}
	_ = ips

	return time.Since(start), nil
}

func averageLatency(ip string, domain string, attempts int) (time.Duration, error) {
	var total time.Duration = 0
	var count int = 0

	for i := 0; i < attempts; i++ {
		latency, err := resolveDNS(ip, domain)
		if err != nil {
			continue
		}
		total += latency
		count++
		time.Sleep(300 * time.Millisecond)
	}

	if count == 0 {
		return 0, fmt.Errorf("all dns query failed")
	}
	return total / time.Duration(count), nil
}

func readDNSList(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var dnsList []string
	reader := bufio.NewReader(file)

	for {
		line, _, err := reader.ReadLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		ip := strings.TrimSpace(string(line))
		if ip != "" {
			dnsList = append(dnsList, ip)
		}
	}
	return dnsList, nil
}

func normalize(d time.Duration, min, max time.Duration) float64 {
	if max == min {
		return 0.5
	}
	return float64(d-min) / float64(max-min)
}

func main() {
	domain := "bilibili.com"
	attempts := 5
	dnsList, err := readDNSList("dns.txt")
	if err != nil {
		log.Fatalf("Failed to read DNS list: %v", err)
	}

	var results []DNSResult
	var mutex sync.Mutex
	var wg sync.WaitGroup

	for _, ip := range dnsList {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			queryLatency, err := averageLatency(ip, domain, attempts)
			if err != nil {
				log.Printf("DNS %s failed: %v", ip, err)
				return
			}

			networkLatency, err := PingNode(ip)
			if err != nil {
				log.Printf("Ping %s failed: %v", ip, err)
				return
			}

			mutex.Lock()
			results = append(results, DNSResult{
				IP:             ip,
				QueryLatency:   queryLatency,
				NetworkLatency: networkLatency,
			})
			mutex.Unlock()
		}(ip)
	}
	wg.Wait()

	var maxQL, minQL time.Duration = 0, math.MaxInt64
	var maxNL, minNL time.Duration = 0, math.MaxInt64

	for _, res := range results {
		if res.QueryLatency > maxQL {
			maxQL = res.QueryLatency
		}
		if res.QueryLatency < minQL {
			minQL = res.QueryLatency
		}
		if res.NetworkLatency > maxNL {
			maxNL = res.NetworkLatency
		}
		if res.NetworkLatency < minNL {
			minNL = res.NetworkLatency
		}
	}

	for i := range results {
		qScore := normalize(results[i].QueryLatency, minQL, maxQL)
		nScore := normalize(results[i].NetworkLatency, minNL, maxNL)
		results[i].Score = 0.6*qScore + 0.4*nScore
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score < results[j].Score
	})

	fmt.Printf("DNS Server\tQuery Latency\tNetwork Latency\tScore\n")
	for _, res := range results {
		fmt.Printf("%-15s %-12v %-14v %.4f\n",
			res.IP,
			res.QueryLatency.Round(time.Millisecond),
			res.NetworkLatency.Round(time.Millisecond),
			res.Score,
		)
	}
}
