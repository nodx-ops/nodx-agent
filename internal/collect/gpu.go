package collect

import (
	"expvar"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var varGPUDisabled = expvar.NewInt("collect_gpu_disabled_total")

// gpuReader shells to nvidia-smi (csv, nounits) and reports the max across
// GPUs per metric — the envelope has no labels. A missing binary disables
// the reader once; "[N/A]" fields (unsupported sensors) are skipped, not
// fatal. Non-NVIDIA GPUs are simply absent (no sample, no error).
type gpuReader struct {
	lookPath func(string) (string, error)
	run      func(time.Duration, string, ...string) ([]byte, error)
	timeout  time.Duration
	disabled bool
}

func newGPUReader(timeout time.Duration) *gpuReader {
	return &gpuReader{lookPath: exec.LookPath, run: runCmd, timeout: timeout}
}

func (r *gpuReader) name() string { return "gpu" }

func (r *gpuReader) read(now time.Time) ([]metricPair, error) {
	_ = now
	if r.disabled {
		return nil, nil
	}
	if _, err := r.lookPath("nvidia-smi"); err != nil {
		r.disabled = true
		varGPUDisabled.Add(1)
		log.Printf("[INFO] collect gpu disabled: nvidia-smi not found (one-time)")
		return nil, nil
	}
	out, err := r.run(r.timeout, "nvidia-smi",
		"--query-gpu=temperature.gpu,utilization.gpu,power.draw",
		"--format=csv,noheader,nounits")
	if err != nil {
		return nil, fmt.Errorf("gpu: nvidia-smi: %w", err)
	}
	return parseSMI(out)
}

// parseSMI folds csv rows "temp, util, power" into per-metric maxima.
// A row of all-N/A yields no sample; a fully empty result is an error so the
// collector emits collect_error_gpu instead of silent nothing.
func parseSMI(out []byte) ([]metricPair, error) {
	names := []string{"gpu_temperature_c", "gpu_utilization_pct", "gpu_power_watts"}
	var max [3]float64
	var found [3]bool
	rows := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rows++
		fields := strings.Split(line, ",")
		for i := 0; i < len(fields) && i < 3; i++ {
			f := strings.TrimSpace(fields[i])
			if f == "[N/A]" || f == "N/A" || f == "" {
				continue
			}
			v, err := strconv.ParseFloat(strings.Fields(f)[0], 64)
			if err != nil {
				continue
			}
			if !found[i] || v > max[i] {
				max[i], found[i] = v, true
			}
		}
	}
	if rows == 0 {
		return nil, fmt.Errorf("gpu: nvidia-smi: empty output")
	}
	var pairs []metricPair
	for i, n := range names {
		if found[i] {
			pairs = append(pairs, pair(n, max[i]))
		}
	}
	return pairs, nil
}
