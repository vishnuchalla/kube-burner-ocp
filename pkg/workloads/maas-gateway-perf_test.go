// Copyright 2026 The Kube-burner Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package workloads

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	sprig "github.com/Masterminds/sprig/v3"
	"gopkg.in/yaml.v3"
)

func TestNormalizePayloadSizes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"presets", "small,medium", "small:32:64,medium:256:512"},
		{"agentic preset", "agentic", "agentic:8192:2048"},
		{"inline pair", "8192:2048", "p8192-o2048:8192:2048"},
		{"mixed", "small,4096:512", "small:32:64,p4096-o512:4096:512"},
		{"whitespace tolerated", " small , 64:16 ", "small:32:64,p64-o16:64:16"},
		{"duplicates collapsed", "small,small", "small:32:64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizePayloadSizes(tc.input); got != tc.want {
				t.Errorf("normalizePayloadSizes(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseThinkTimeDisabledWhenEmpty(t *testing.T) {
	if tt := parseThinkTime(""); tt.enabled {
		t.Fatalf("empty --think-time should be disabled, got %+v", tt)
	}
}

func TestParseThinkTimeDerivesAverageAndSpread(t *testing.T) {
	tt := parseThinkTime("15s,45s")
	if !tt.enabled {
		t.Fatal("expected think time to be enabled")
	}
	if tt.min != 15 || tt.max != 45 {
		t.Errorf("min/max = %v/%v, want 15/45", tt.min, tt.max)
	}
	if tt.avg != 30 {
		t.Errorf("avg = %v, want 30", tt.avg)
	}
	// +/-2 sigma spans the requested window before clamping.
	if tt.stdev != 7.5 {
		t.Errorf("stdev = %v, want 7.5", tt.stdev)
	}
}

func TestParseThinkTimeZeroStdevForDegenerateWindow(t *testing.T) {
	// A min==max window must yield a zero stdev so the job template omits the
	// delay_stdev key, which guidellm rejects when it is not greater than zero.
	tt := parseThinkTime("30s,30s")
	if tt.stdev != 0 {
		t.Errorf("stdev = %v, want 0", tt.stdev)
	}
}

func TestAutoTurnsCoversMeasurementWindow(t *testing.T) {
	tt := parseThinkTime("15s,45s") // avg 30s
	if got := autoTurns(tt, 300*time.Second); got != 11 {
		t.Errorf("autoTurns = %d, want 11", got)
	}
	if got := autoTurns(thinkTime{}, 300*time.Second); got != 1 {
		t.Errorf("autoTurns without think time = %d, want 1", got)
	}
	// Never collapse to a single turn, which would make think time a no-op.
	if got := autoTurns(tt, time.Second); got != 2 {
		t.Errorf("autoTurns for a short window = %d, want 2", got)
	}
}

func TestIdentityPodsRoundsUp(t *testing.T) {
	for _, tc := range []struct {
		identities, keysPerPod, want int
	}{
		{50, 1, 50},
		{50, 10, 5},
		{55, 10, 6},
		{8000, 100, 80},
		{0, 10, 1},
	} {
		if got := identityPods(tc.identities, tc.keysPerPod); got != tc.want {
			t.Errorf("identityPods(%d, %d) = %d, want %d", tc.identities, tc.keysPerPod, got, tc.want)
		}
	}
}

func renderMaasTemplate(t *testing.T, name string, vars map[string]any, strict bool) string {
	t.Helper()
	path := filepath.Join("..", "..", "cmd", "config", "maas-gateway-perf", name)
	templateBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", name, err)
	}
	tmpl := template.New(name).Funcs(sprig.TxtFuncMap())
	if strict {
		tmpl = tmpl.Option("missingkey=error")
	}
	tmpl, err = tmpl.Parse(string(templateBytes))
	if err != nil {
		t.Fatalf("failed to parse %s: %v", name, err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, vars); err != nil {
		t.Fatalf("failed to render %s: %v", name, err)
	}
	var doc any
	if err := yaml.Unmarshal(rendered.Bytes(), &doc); err != nil {
		t.Fatalf("%s rendered invalid YAML: %v\n---\n%s", name, err, rendered.String())
	}
	return rendered.String()
}

func maasConfigVars() map[string]any {
	return map[string]any{
		"GUIDELLM_IMAGE":     "quay.io/example/guidellm:test",
		"ES_SERVER":          "https://es.example.com",
		"ES_INDEX":           "maas-gateway-perf",
		"LOCAL_INDEXING":     false,
		"METRICS":            "maas-gateway-perf-metrics.yml",
		"ALERTS":             "",
		"GC":                 true,
		"GC_METRICS":         false,
		"QPS":                20,
		"BURST":              20,
		"UUID":               "test-uuid",
		"SAMPLES":            3,
		"PARALLELISM":        5,
		"COMPLETIONS":        15,
		"BENCHMARK_DURATION": 90,
		"WARMUP":             30,
		"PAUSE":              "10s",
		"JOB_DEADLINE":       900,
		"SIMULATOR_HOST":     "http://sim.svc.cluster.local:8000",
		"GATEWAY_HOST":       "https://maas.apps.example.com",
		"GATEWAY_HOSTNAME":   "maas.apps.example.com",
		"GATEWAY_IP":         "172.30.1.1",
		"PROVIDERS":          "gpt-4o-openai",
		"CONCURRENCY_LEVELS": "8,32",
		"LOAD_PROFILES":      "constant",
		"PAYLOAD_SPECS":      "small:32:64,p8192-o2048:8192:2048",
		"TARGETS":            "baseline,gateway",
		"API_KEY_SECRET":     "maas-perf-identities",
		"IDENTITIES":         50,
		"KEYS_PER_POD":       10,
		"POD_CPU_MILLIS":     2500,
		"POD_MEMORY_MI":      2560,
		"METERING_NAMESPACE": "enmaas",
		"TOKENIZER":          "gpt2",
		"SEED_REQUESTS":      0,
		"SEED_COMPLETIONS":   5,
		"TURNS":              1,
		"THINK_TIME_ENABLED": false,
		"THINK_TIME_AVG":     "0",
		"THINK_TIME_MIN":     "0",
		"THINK_TIME_MAX":     "0",
		"THINK_TIME_STDEV":   "0",
	}
}

func TestMaasConfigRendersPayloadSpecsIntoJobNames(t *testing.T) {
	out := renderMaasTemplate(t, "maas-gateway-perf.yml", maasConfigVars(), true)
	for _, want := range []string{
		"name: baseline-small-gpt-4o-openai-c8",
		"name: gateway-p8192-o2048-gpt-4o-openai-c32",
		"BENCH_PROMPT_TOKENS: \"8192\"",
		"BENCH_OUTPUT_TOKENS: \"2048\"",
		"keysPerPod: 10",
		"podCPUMillis: 2500",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q", want)
		}
	}
	if strings.Contains(out, "tokenizerConfigMap") {
		t.Error("rendered config still references the removed tokenizerConfigMap")
	}
	if strings.Contains(out, "name: seed-usage") {
		t.Error("seed job rendered even though --seed-requests-per-identity is 0")
	}
}

func TestMaasConfigRendersSeedJobAndThinkTime(t *testing.T) {
	vars := maasConfigVars()
	vars["SEED_REQUESTS"] = 30
	vars["TURNS"] = 11
	vars["THINK_TIME_ENABLED"] = true
	vars["THINK_TIME_AVG"] = "30"
	vars["THINK_TIME_MIN"] = "15"
	vars["THINK_TIME_MAX"] = "45"
	vars["THINK_TIME_STDEV"] = "7.5"
	out := renderMaasTemplate(t, "maas-gateway-perf.yml", vars, true)
	for _, want := range []string{
		"name: seed-usage",
		"BENCH_SEED_REQUESTS: \"30\"",
		"BENCH_TURNS: \"11\"",
		"BENCH_THINK_TIME: \"true\"",
		"BENCH_DELAY_MIN: \"15\"",
		"BENCH_DELAY_STDEV: \"7.5\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q", want)
		}
	}
}

func TestMaasConfigHonoursTargetSelection(t *testing.T) {
	vars := maasConfigVars()
	vars["TARGETS"] = "gateway"
	out := renderMaasTemplate(t, "maas-gateway-perf.yml", vars, true)
	if strings.Contains(out, "name: baseline-") {
		t.Error("baseline jobs rendered when --targets excludes baseline")
	}
	if !strings.Contains(out, "name: gateway-") {
		t.Error("gateway jobs missing when --targets selects gateway")
	}
}

func maasJobVars() map[string]any {
	return map[string]any{
		"jobName":         "gateway-small-gpt-4o-openai-c8",
		"Replica":         1,
		"completions":     15,
		"parallelism":     5,
		"jobDeadline":     900,
		"gatewayIP":       "172.30.1.1",
		"gatewayHostname": "maas.apps.example.com",
		"apiKeySecret":    "maas-perf-identities",
		"keysPerPod":      10,
		"podCPUMillis":    2500,
		"podMemoryMi":     2560,
		"tokenizer":       "gpt2",
		"image":           "quay.io/example/guidellm:test",
		"ES_SERVER":       "https://es.example.com",
		"ES_INDEX":        "maas-gateway-perf",
		"UUID":            "test-uuid",
		"env": map[string]any{
			"BENCH_TARGET":        "https://maas.apps.example.com",
			"BENCH_MAX_SECONDS":   "90",
			"BENCH_WARMUP":        "30",
			"BENCH_PROFILE":       "constant",
			"BENCH_RATE":          "8",
			"BENCH_PROMPT_TOKENS": "32",
			"BENCH_OUTPUT_TOKENS": "64",
			"BENCH_MODEL":         "gpt-4o-openai",
		},
	}
}

func TestMaasJobTemplateScalesResourcesWithKeysPerPod(t *testing.T) {
	out := renderMaasTemplate(t, "guidellm-job.yml", maasJobVars(), false)
	for _, want := range []string{
		`cpu: "2500m"`,
		"memory: 2560Mi",
		`value: "10"`, // BENCH_KEYS_PER_POD
		"secretName: maas-perf-identities",
		"mountPath: /identities",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered Job missing %q", want)
		}
	}
	for _, unwanted := range []string{"tokenizer.tar.gz", "HF_HUB_OFFLINE", "TRANSFORMERS_OFFLINE", "configMap:"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("rendered Job still references removed tokenizer ConfigMap support: %q", unwanted)
		}
	}
}

// TestMaasEveryJobRendersItsObjectTemplate takes the inputVars the config
// actually emits for every job and feeds them back through guidellm-job.yml
// under strict variable lookup, which is how kube-burner renders object
// templates. A job that forgets an inputVar fails here rather than on a cluster.
func TestMaasEveryJobRendersItsObjectTemplate(t *testing.T) {
	vars := maasConfigVars()
	vars["SEED_REQUESTS"] = 30
	vars["SEED_COMPLETIONS"] = 5
	rendered := renderMaasTemplate(t, "maas-gateway-perf.yml", vars, true)

	var config struct {
		Jobs []struct {
			Name    string `yaml:"name"`
			Objects []struct {
				ObjectTemplate string         `yaml:"objectTemplate"`
				InputVars      map[string]any `yaml:"inputVars"`
			} `yaml:"objects"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &config); err != nil {
		t.Fatalf("failed to parse rendered config: %v", err)
	}

	var seen int
	for _, job := range config.Jobs {
		for _, object := range job.Objects {
			if object.ObjectTemplate != "guidellm-job.yml" {
				continue
			}
			seen++
			jobVars := map[string]any{"Replica": 1, "UUID": vars["UUID"]}
			for k, v := range object.InputVars {
				jobVars[k] = v
			}
			t.Run(job.Name, func(t *testing.T) {
				renderMaasTemplate(t, "guidellm-job.yml", jobVars, true)
			})
		}
	}
	// 1 seed + (2 payloads x 2 levels x 1 provider x 1 profile x 2 targets).
	if seen != 9 {
		t.Errorf("rendered %d guidellm Jobs, want 9", seen)
	}
}

func TestMaasSeedJobSweepsPoolOnce(t *testing.T) {
	vars := maasConfigVars()
	vars["SEED_REQUESTS"] = 30
	vars["SEED_COMPLETIONS"] = 5 // one completion per pod...
	vars["COMPLETIONS"] = 15     // ...while the benchmark keeps samples x pods
	out := renderMaasTemplate(t, "maas-gateway-perf.yml", vars, true)
	seedBlock := out[strings.Index(out, "name: seed-usage"):]
	seedBlock = seedBlock[:strings.Index(seedBlock, "name: baseline-")]
	if !strings.Contains(seedBlock, "completions: 5") {
		t.Errorf("seed job should sweep the pool once (completions: 5), got:\n%s", seedBlock)
	}
	if strings.Contains(seedBlock, "completions: 15") {
		t.Error("seed job inherited the benchmark's samples-per-pod completions")
	}
}

func TestMaasJobTemplateWithoutIdentityPool(t *testing.T) {
	vars := maasJobVars()
	delete(vars, "apiKeySecret")
	delete(vars, "gatewayIP")
	out := renderMaasTemplate(t, "guidellm-job.yml", vars, false)
	if strings.Contains(out, "mountPath: /identities") {
		t.Error("identity volume mounted without an --api-key-secret")
	}
	if strings.Contains(out, "hostAliases") {
		t.Error("hostAliases rendered without a --gateway-ip, which should exercise the edge path")
	}
	if !strings.Contains(out, "serviceaccount") {
		t.Error("ServiceAccount fallback missing")
	}
}
