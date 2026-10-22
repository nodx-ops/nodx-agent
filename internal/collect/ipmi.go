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

var varIPMIDisabled = expvar.NewInt("collect_ipmi_disabled_total")

// ipmiReader shells to ipmitool (dcmi power + SDR temperatures). A missing
// binary disables the reader once (log + counter, then silent skip) —
// bare-metal-only tooling must not spam VMs/containers every tick.
type ipmiReader struct {
	lookPath func(string) (string, error)
	run      func(time.Duration, string, ...string) ([]byte, error)
	timeout  time.Duration
	disabled bool
}

func newIPMIReader(timeout time.Duration) *ipmiReader {
	return &ipmiReader{lookPath: exec.LookPath, run: runCmd, timeout: timeout}
}

func (r *ipmiReader) name() string { return "ipmi" }

func (r *ipmiReader) read(now time.Time) ([]metricPair, error) {
	_ = now
	if r.disabled {
		return nil, nil
	}
	if _, err := r.lookPath("ipmitool"); err != nil {
		r.disabled = true
		varIPMIDisabled.Add(1)
		log.Printf("[INFO] collect ipmi disabled: ipmitool not found (one-time)")
		return nil, nil
	}
	var out []metricPair
	var powerErr, tempErr error
	if w, err := r.readDCMI(); err != nil {
		powerErr = err
	} else {
		out = append(out, pair("power_draw_watts", w))
	}
	if t, ok, err := r.readSDRTemps(); err != nil {
		tempErr = err
	} else if ok {
		out = append(out, pair("bmc_temperature_c", t))
	}
	if len(out) == 0 {
		if powerErr != nil {
			return nil, powerErr
		}
		return nil, tempErr
	}
	return out, nil
}

func (r *ipmiReader) readDCMI() (float64, error) {
	out, err := r.run(r.timeout, "ipmitool", "dcmi", "power", "reading")
	if err != nil {
		return 0, fmt.Errorf("ipmi: dcmi: %w", err)
	}
	return parseDCMIPower(out)
}

func (r *ipmiReader) readSDRTemps() (float64, bool, error) {
	out, err := r.run(r.timeout, "ipmitool", "sdr", "type", "Temperature")
	if err != nil {
		return 0, false, fmt.Errorf("ipmi: sdr: %w", err)
	}
	return parseSDRTemps(out)
}

// parseDCMIPower extracts "Average power draw over sample period: 145 Watts".
func parseDCMIPower(out []byte) (float64, error) {
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "Average power") {
			continue
		}
		for _, f := range strings.Fields(line) {
			if v, err := strconv.ParseFloat(f, 64); err == nil {
				return v, nil
			}
		}
	}
	return 0, fmt.Errorf("ipmi: dcmi: no average power line")
}

// parseSDRTemps returns the max "degrees C" reading from sdr output lines
// like "CPU Temp | 45 degrees C | ok". ok=false when no sensors matched.
func parseSDRTemps(out []byte) (float64, bool, error) {
	var max float64
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		idx := strings.Index(line, "degrees C")
		if idx < 0 {
			continue
		}
		head := line[:idx]
		fields := strings.Fields(head)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		if !found || v > max {
			max, found = v, true
		}
	}
	return max, found, nil
}
