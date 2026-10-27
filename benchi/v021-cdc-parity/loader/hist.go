package main

import (
	"math"
)

// hist is a log-bucketed latency histogram: bucket b holds latencies within
// 2% of 1.02^b microseconds, so any percentile read from it is within 2% of
// the exact value. Histograms are per second of ARRIVAL time, so analysis can
// merge exactly the seconds of any window.
type hist map[int]int64

func latBucket(us int64) int {
	if us < 1 {
		us = 1
	}
	return int(math.Round(math.Log(float64(us)) / math.Log(1.02)))
}

func (h hist) add(us int64) { h[latBucket(us)]++ }

// secHists is per-arrival-second histograms plus counts and bytes.
type secHists map[int64]*secHist

type secHist struct {
	N     int64 `json:"n"`
	Bytes int64 `json:"bytes"`
	Lat   hist  `json:"lat"`
	NoMk  int64 `json:"no_marker"` // records without a marker (deletes, tombstones)
}

func (s secHists) add(arrivalMs int64, bytes int, markerTS int64) {
	sec := arrivalMs / 1000
	h := s[sec]
	if h == nil {
		h = &secHist{Lat: hist{}}
		s[sec] = h
	}
	h.N++
	h.Bytes += int64(bytes)
	if markerTS > 0 {
		h.Lat.add((arrivalMs*1000 - markerTS/1000))
	} else {
		h.NoMk++
	}
}
