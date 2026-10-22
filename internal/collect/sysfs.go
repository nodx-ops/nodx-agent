package collect

import (
	"expvar"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Allowlisted roots: every path opened below is constructed from fixed names
// under these roots, never from external input.
var (
	procRoot = "/proc"
	sysRoot  = "/sys"
)

var varSysfsSkipped = expvar.NewInt("collect_sysfs_skipped_total")

type energyMark struct {
	uj uint64
	at time.Time
}

// sysfsReader gathers host metrics from procfs/sysfs without subprocesses:
// load, memory, hwmon temperatures/fans (max across sensors — the envelope
// has no labels, so aggregates only), and powercap energy deltas.
type sysfsReader struct {
	proc string
	sys  string
	last map[string]energyMark
}

func newSysfsReader() *sysfsReader {
	return &sysfsReader{proc: procRoot, sys: sysRoot, last: make(map[string]energyMark)}
}

func (r *sysfsReader) name() string { return "sysfs" }

func (r *sysfsReader) read(now time.Time) ([]metricPair, error) {
	var out []metricPair
	var firstErr error
	emit := func(p metricPair, err error) {
		if err != nil {
			varSysfsSkipped.Add(1)
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		out = append(out, p)
	}

	if v, err := readLoadAvg(r.proc); err != nil {
		emit(metricPair{}, err)
	} else {
		emit(pair("cpu_load_1m", v), nil)
	}
	if used, avail, err := readMemInfo(r.proc); err != nil {
		emit(metricPair{}, err)
	} else {
		emit(pair("mem_used_bytes", float64(used)), nil)
		emit(pair("mem_available_bytes", float64(avail)), nil)
	}
	if v, ok, err := readHwmonMax(r.sys, "temp"); err != nil {
		emit(metricPair{}, err)
	} else if ok {
		emit(pair("temperature_c", v), nil)
	}
	if v, ok, err := readHwmonMax(r.sys, "fan"); err != nil {
		emit(metricPair{}, err)
	} else if ok {
		emit(pair("fan_speed_rpm", v), nil)
	}
	// powercap_draw_watts is the host estimate; BMC power (when present) owns
	// the canonical power_draw_watts name — first-reader-wins would collide.
	if v, ok, err := r.readPowercap(now); err != nil {
		emit(metricPair{}, err)
	} else if ok {
		emit(pair("powercap_draw_watts", v), nil)
	}

	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func readLoadAvg(proc string) (float64, error) {
	raw, err := os.ReadFile(filepath.Join(proc, "loadavg"))
	if err != nil {
		return 0, fmt.Errorf("sysfs: loadavg: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 1 {
		return 0, fmt.Errorf("sysfs: loadavg: empty")
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("sysfs: loadavg: %w", err)
	}
	return v, nil
}

func readMemInfo(proc string) (usedBytes, availBytes uint64, err error) {
	raw, err := os.ReadFile(filepath.Join(proc, "meminfo"))
	if err != nil {
		return 0, 0, fmt.Errorf("sysfs: meminfo: %w", err)
	}
	var total, avail uint64
	var haveTotal, haveAvail bool
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, perr := strconv.ParseUint(fields[1], 10, 64)
		if perr != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total, haveTotal = v*1024, true
		case "MemAvailable:":
			avail, haveAvail = v*1024, true
		}
	}
	if !haveTotal || !haveAvail || avail > total {
		return 0, 0, fmt.Errorf("sysfs: meminfo: missing MemTotal/MemAvailable")
	}
	return total - avail, avail, nil
}

// readHwmonMax returns the max sensor value across hwmon devices.
// kind "temp" reads temp*_input (millidegree C); kind "fan" reads fan*_input
// (RPM). ok=false when no sensors exist (not an error: fanless hosts).
func readHwmonMax(sys, kind string) (v float64, ok bool, err error) {
	entries, rerr := os.ReadDir(filepath.Join(sys, "class", "hwmon"))
	if rerr != nil {
		return 0, false, fmt.Errorf("sysfs: hwmon: %w", rerr)
	}
	var max float64
	found := false
	for _, e := range entries {
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		dir := filepath.Join(sys, "class", "hwmon", e.Name())
		files, rerr := os.ReadDir(dir)
		if rerr != nil {
			varSysfsSkipped.Add(1)
			continue
		}
		for _, f := range files {
			n := f.Name()
			if !strings.HasPrefix(n, kind) || !strings.HasSuffix(n, "_input") {
				continue
			}
			// tempN_label-style companions end in _input too? No: only
			// tempN_input/fanN_input match; labels are tempN_label.
			raw, rerr := os.ReadFile(filepath.Join(dir, n))
			if rerr != nil {
				varSysfsSkipped.Add(1)
				continue
			}
			iv, perr := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
			if perr != nil {
				varSysfsSkipped.Add(1)
				continue
			}
			if kind == "temp" {
				iv /= 1000 // millidegree C
			}
			if !found || iv > max {
				max, found = iv, true
			}
		}
	}
	if !found {
		return 0, false, nil
	}
	return max, true, nil
}

// readPowercap converts RAPL energy_uj deltas to watts per zone and reports
// the max. Zones without a prior mark are seeded (no sample on first poll);
// counter resets reseed instead of emitting a spike.
func (r *sysfsReader) readPowercap(now time.Time) (float64, bool, error) {
	base := filepath.Join(r.sys, "class", "powercap")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil // no RAPL support: fanless/VM hosts
		}
		return 0, false, fmt.Errorf("sysfs: powercap: %w", err)
	}
	var max float64
	found := false
	seen := make(map[string]bool)
	visit := func(zone, dir string) {
		seen[zone] = true
		raw, rerr := os.ReadFile(filepath.Join(dir, "energy_uj"))
		if rerr != nil {
			varSysfsSkipped.Add(1)
			return
		}
		uj, perr := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if perr != nil {
			varSysfsSkipped.Add(1)
			return
		}
		mark, ok := r.last[zone]
		r.last[zone] = energyMark{uj: uj, at: now}
		if !ok {
			return // seeded: no delta yet
		}
		dt := now.Sub(mark.at).Seconds()
		if dt <= 0 || uj < mark.uj {
			return // clock step or counter reset: reseed
		}
		w := float64(uj-mark.uj) / 1e6 / dt
		if !found || w > max {
			max, found = w, true
		}
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "intel-rapl") {
			continue
		}
		dir := filepath.Join(base, e.Name())
		visit(e.Name(), dir)
		sub, rerr := os.ReadDir(dir)
		if rerr != nil {
			continue
		}
		for _, s := range sub {
			if strings.HasPrefix(s.Name(), "intel-rapl") {
				visit(e.Name()+"/"+s.Name(), filepath.Join(dir, s.Name()))
			}
		}
	}
	// Drop marks for vanished zones so the map cannot grow unboundedly.
	for zone := range r.last {
		if !seen[zone] {
			delete(r.last, zone)
		}
	}
	if !found {
		return 0, false, nil
	}
	return max, true, nil
}
