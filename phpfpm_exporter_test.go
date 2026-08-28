// Copyright 2017 Kumina, https://kumina.nl/
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestNewSocketPath(t *testing.T) {
	cases := []struct {
		input           string
		expectedNetwork string
		expectedAddress string
	}{
		{"/var/run/php-fpm.sock", "unix", "/var/run/php-fpm.sock"},
		{"unix:///var/run/php-fpm.sock", "unix", "/var/run/php-fpm.sock"},
		{"tcp://127.0.0.1:9000", "tcp", "127.0.0.1:9000"},
	}

	for _, c := range cases {
		got := NewSocketPath(c.input)
		if got.Network != c.expectedNetwork || got.Address != c.expectedAddress {
			t.Errorf("NewSocketPath(%q) = {%q, %q}, want {%q, %q}",
				c.input, got.Network, got.Address, c.expectedNetwork, c.expectedAddress)
		}
	}
}

func TestSocketPathFormatStr(t *testing.T) {
	s := &SocketPath{Network: "unix", Address: "/var/run/php-fpm.sock"}
	want := "unix:///var/run/php-fpm.sock"
	if got := s.FormatStr(); got != want {
		t.Errorf("FormatStr() = %q, want %q", got, want)
	}
}

const sampleStatus = `pool:                 www
process manager:      dynamic
start time:            29/Aug/2026:10:00:00 +0000
start since:           12345
accepted conn:         100
listen queue:          0
max listen queue:      5
listen queue len:      128
idle processes:        2
active processes:      3
total processes:       5
max active processes:  4
max children reached:  0
slow requests:         1
************************
pid:                  1
state:                Idle
************************
pid:                  2
state:                Idle
************************
pid:                  3
state:                Running
`

func collectMetrics(t *testing.T, reader *strings.Reader, socketPath string) []prometheus.Metric {
	t.Helper()

	ch := make(chan prometheus.Metric, 64)
	if err := CollectStatusFromReader(reader, socketPath, ch); err != nil {
		t.Fatalf("CollectStatusFromReader() error = %v", err)
	}
	close(ch)

	var metrics []prometheus.Metric
	for m := range ch {
		metrics = append(metrics, m)
	}
	return metrics
}

func findMetric(t *testing.T, metrics []prometheus.Metric, desc *prometheus.Desc, labelValue string) *dto.Metric {
	t.Helper()

	for _, m := range metrics {
		if m.Desc() != desc {
			continue
		}
		out := &dto.Metric{}
		if err := m.Write(out); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if labelValue == "" {
			return out
		}
		for _, l := range out.Label {
			if l.GetName() == "state" && l.GetValue() == labelValue {
				return out
			}
		}
	}
	return nil
}

func TestCollectStatusFromReader(t *testing.T) {
	const socketPath = "unix:///var/run/php-fpm.sock"

	metrics := collectMetrics(t, strings.NewReader(sampleStatus), socketPath)

	cases := []struct {
		name      string
		desc      *prometheus.Desc
		value     float64
		isCounter bool
	}{
		{"accepted conn", phpfpmAcceptedConnections, 100, true},
		{"listen queue", phpfpmGauges["listen queue"], 0, false},
		{"max listen queue", phpfpmGauges["max listen queue"], 5, false},
		{"listen queue len", phpfpmGauges["listen queue len"], 128, false},
		{"idle processes", phpfpmGauges["idle processes"], 2, false},
		{"active processes", phpfpmGauges["active processes"], 3, false},
		{"total processes", phpfpmGauges["total processes"], 5, false},
		{"max active processes", phpfpmGauges["max active processes"], 4, false},
		{"max children reached", phpfpmGauges["max children reached"], 0, false},
		{"slow requests", phpfpmGauges["slow requests"], 1, false},
	}

	for _, c := range cases {
		out := findMetric(t, metrics, c.desc, "")
		if out == nil {
			t.Errorf("%s: metric not found", c.name)
			continue
		}

		if c.isCounter {
			if out.Counter == nil {
				t.Errorf("%s: expected a Counter metric, got %v", c.name, out)
				continue
			}
		} else if out.Gauge == nil {
			t.Errorf("%s: expected a Gauge metric, got %v", c.name, out)
			continue
		}

		got := out.GetGauge().GetValue()
		if c.isCounter {
			got = out.GetCounter().GetValue()
		}
		if got != c.value {
			t.Errorf("%s = %v, want %v", c.name, got, c.value)
		}
	}

	wantStart := time.Date(2026, time.August, 29, 10, 0, 0, 0, time.UTC).Unix()
	startMetric := findMetric(t, metrics, phpfpmStartTime, "")
	if startMetric == nil {
		t.Fatal("start time metric not found")
	}
	if got := startMetric.GetGauge().GetValue(); got != float64(wantStart) {
		t.Errorf("start time = %v, want %v", got, wantStart)
	}

	stateCases := map[string]float64{
		"Idle":    2,
		"Running": 1,
	}
	for state, want := range stateCases {
		out := findMetric(t, metrics, phpfpmStateCountDesc, state)
		if out == nil {
			t.Errorf("state count for %q: metric not found", state)
			continue
		}
		if got := out.GetGauge().GetValue(); got != want {
			t.Errorf("state count for %q = %v, want %v", state, got, want)
		}
	}
}

func TestCollectStatusFromReaderIgnoresUnparsableLines(t *testing.T) {
	const socketPath = "unix:///var/run/php-fpm.sock"
	input := "not a valid line\n\naccepted conn:         42\n"

	metrics := collectMetrics(t, strings.NewReader(input), socketPath)

	out := findMetric(t, metrics, phpfpmAcceptedConnections, "")
	if out == nil {
		t.Fatal("accepted conn metric not found")
	}
	if got := out.GetCounter().GetValue(); got != 42 {
		t.Errorf("accepted conn = %v, want 42", got)
	}
}

func TestCollectStatusFromReaderInvalidNumber(t *testing.T) {
	const socketPath = "unix:///var/run/php-fpm.sock"
	input := "accepted conn:         not-a-number\n"

	ch := make(chan prometheus.Metric, 8)
	err := CollectStatusFromReader(strings.NewReader(input), socketPath, ch)
	close(ch)
	if err == nil {
		t.Fatal("expected an error for a non-numeric value, got nil")
	}
}
