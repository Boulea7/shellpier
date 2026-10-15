//go:build shellpier_runtime_canary

// This independent diagnostic contains no third-party reproducer source.
// The harness supplies blob.bin in an isolated temporary module.
package main

import (
	_ "embed"
	"fmt"
	"os"
	"runtime"
	"sync"
)

//go:embed blob.bin
var imageData string

// init traverses the embedded read-only image before it has been warmed by main.
// Each word depends on its position, so replayed loads or index updates are visible.
func init() {
	fmt.Printf("CANARY_START go=%s image_bytes=%d procs=%d\n", runtime.Version(), len(imageData), runtime.GOMAXPROCS(0))
	if len(imageData) == 0 || len(imageData)%8 != 0 {
		fmt.Fprintln(os.Stderr, "CANARY_INVALID_IMAGE")
		os.Exit(2)
	}
	for offset := 0; offset < len(imageData); offset += 8 {
		actual := uint64(imageData[offset]) | uint64(imageData[offset+1])<<8 |
			uint64(imageData[offset+2])<<16 | uint64(imageData[offset+3])<<24 |
			uint64(imageData[offset+4])<<32 | uint64(imageData[offset+5])<<40 |
			uint64(imageData[offset+6])<<48 | uint64(imageData[offset+7])<<56
		expected := (uint64(offset/8) ^ uint64(0xd6e8feb86659fd93)) * uint64(0xa0761d6478bd642f)
		if actual != expected {
			fmt.Fprintf(os.Stderr, "CANARY_IMAGE_MISMATCH offset=%d actual=%016x expected=%016x\n", offset, actual, expected)
			os.Exit(1)
		}
	}
}

func main() {
	const workers, iterations = 4, 100000
	var lock sync.Mutex
	values := make(map[int]int)
	var done sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		done.Add(1)
		go func(key int) {
			defer done.Done()
			for i := 0; i < iterations; i++ {
				lock.Lock()
				values[key]++
				lock.Unlock()
				if i%1024 == 0 {
					runtime.Gosched()
				}
			}
		}(worker)
	}
	done.Wait()
	for key := 0; key < workers; key++ {
		if values[key] != iterations {
			fmt.Fprintf(os.Stderr, "CANARY_MAP_MISMATCH key=%d actual=%d expected=%d\n", key, values[key], iterations)
			os.Exit(1)
		}
	}
	fmt.Println("CANARY_OK")
}
