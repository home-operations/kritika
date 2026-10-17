package configfile

import (
	"testing"
	"time"
)

func TestFileRuntime(t *testing.T) {
	setInstanceEnv(t)
	f := mustLoad(t, minimal+`
webUrl: https://kritika.example.com
pollInterval: 0s
pollLookback: 2h
onboardWindow: 8
indexGrace: 48h
transcriptRetention: 72h
diffRetention: 96h
runner:
  jobTemplate: {spec: {activeDeadlineSeconds: 1200}}
  podTemplate: {spec: {resources: {limits: {memory: 1Gi}}}}
`)
	spec, _ := f.Run.RunnerPodTemplate["spec"].(map[string]any)
	if f.PollInterval() != 0 || f.PollLookback() != 2*time.Hour || f.OnboardWindow() != 8 ||
		f.DisabledIndexGrace() != 48*time.Hour || f.TranscriptRetention() != 72*time.Hour || f.DiffRetention() != 96*time.Hour ||
		f.RunnerDeadline() != 20*time.Minute || spec["resources"] == nil {
		t.Fatalf("runtime settings = %+v", f.Run)
	}
	for _, doc := range []string{
		"runner: {deadlien: 20m}",
		"runner: {jobTemplate: {spec: {activeDeadlineSeconds: 0}}}",
		"onboardWindow: 0",
		"transcriptRetention: 1h",
		"webUrll: https://example.com",
		"databaseUrl: postgres://secret@db/kritika",
	} {
		t.Run(doc, func(t *testing.T) {
			if _, err := Parse([]byte(minimal + doc)); err == nil {
				t.Fatal("invalid file accepted")
			}
		})
	}
}
