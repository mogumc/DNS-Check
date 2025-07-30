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
	ConnectLatency time.Duration
	Score          float64
}

func resolveDNS(ip, domain string) (time.Duration, []string, time.Duration, error) {
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
		return 0, nil, 0, err
	}

	queryLatency := time.Since(start)

	var minConnectLatency time.Duration = math.MaxInt64
	for _, host := range ips {
		conn, err := net.DialTimeout("tcp", host+":80", 5*time.Second)
		if err == nil {
			latency := time.Since(start)
			if latency < minConnectLatency {
				minConnectLatency = latency
			}
			conn.Close()
		}
	}

	if minConnectLatency == math.MaxInt64 {
		minConnectLatency = 0
	}

	return queryLatency, ips, minConnectLatency, nil
}

func averageLatency(ip string, domain string, attempts int) (time.Duration, []string, time.Duration, error) {
	var total time.Duration = 0
	var connectLatency time.Duration = math.MaxInt64
	var count int = 0
	var resolvedIPs []string

	for i := 0; i < attempts; i++ {
		ql, ips, cl, err := resolveDNS(ip, domain)
		if err != nil {
			continue
		}
		total += ql
		if cl < connectLatency {
			connectLatency = cl
		}
		resolvedIPs = ips
		count++
		time.Sleep(300 * time.Millisecond)
	}

	if count == 0 {
		return 0, nil, 0, fmt.Errorf("all dns query failed")
	}
	return total / time.Duration(count), resolvedIPs, connectLatency, nil
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
	attempts := 3

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

			queryLatency, _, connectLatency, err := averageLatency(ip, domain, attempts)
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
				ConnectLatency: connectLatency,
			})
			mutex.Unlock()
		}(ip)
	}
	wg.Wait()

	var maxQL, minQL = time.Duration(0), time.Duration(math.MaxInt64)
	var maxNL, minNL = time.Duration(0), time.Duration(math.MaxInt64)
	var maxCL, minCL = time.Duration(0), time.Duration(math.MaxInt64)

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

		if res.ConnectLatency > maxCL {
			maxCL = res.ConnectLatency
		}
		if res.ConnectLatency < minCL && res.ConnectLatency != 0 {
			minCL = res.ConnectLatency
		}
	}
	if minCL == math.MaxInt64 {
		minCL = 0
	}

	for i := range results {
		qScore := normalize(results[i].QueryLatency, minQL, maxQL)
		nScore := normalize(results[i].NetworkLatency, minNL, maxNL)
		cScore := normalize(results[i].ConnectLatency, minCL, maxCL)
		results[i].Score = 0.4*qScore + 0.3*nScore + 0.3*cScore
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score < results[j].Score
	})

	fmt.Printf("DNS Server\tQuery Latency\tNetwork Latency\tConnect Latency\tScore\n")
	for _, res := range results {
		fmt.Printf("%-15s %-14v %-16v %-15v %.4f\n",
			res.IP,
			res.QueryLatency.Round(time.Millisecond),
			res.NetworkLatency.Round(time.Millisecond),
			res.ConnectLatency.Round(time.Millisecond),
			res.Score,
		)
	}
}
