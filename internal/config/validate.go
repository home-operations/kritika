package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gookit/validate/v2"
	"github.com/minio/minio-go/v7/pkg/s3utils"
)

// minRetention keeps transcripts and diffs available for at least a day.
const minRetention = 24 * time.Hour

// The comparison rules do not convert ByteSize, unlike time.Duration.
func init() {
	validate.AddCustomType(func(field reflect.Value) any { return field.Int() }, ByteSize(0))
}

// Validate makes sure that values meet the runtime bounds that the process
// and file loaders share.
func (s Settings) Validate() error {
	s.LogLevel = strings.ToLower(s.LogLevel)
	s.LogFormat = strings.ToLower(s.LogFormat)
	v := validate.Struct(s)
	v.StopOnError = false
	v.SkipOnEmpty = false
	v.AddRule("GatewayTokenTTL,GatewayCacheMaxSize,GatewayCacheMaxEntrySize,LeaderRetryInterval,AquaRegistryRefresh", "gt", 0)
	v.AddRule("ReviewWorkers,IndexWorkers,ThreadWorkers,OnboardWindow", "gt", 0)
	v.AddRule("GatewayCacheStorage", "enum", []string{"filesystem", "s3"})
	v.AddRule("PollInterval,PollLookback,IndexGrace", "gte", 0)
	v.AddRule("TranscriptRetention,DiffRetention", "min", minRetention)
	v.AddRule("LogLevel", "enum", slices.Sorted(maps.Keys(logLevels)))
	v.AddRule("LogFormat", "enum", []string{"json", "text"})
	v.AddRule("Executor", "enum", []string{string(ExecutorKubernetes), string(ExecutorLocal)})
	v.AddRule("Addr,MetricsAddr,GatewayAddr,DatabaseAppRole,DatabaseRunnerRole", "required")
	v.AddRule("DatabaseRunnerRole", "neField", "DatabaseAppRole")
	v.WithMessages(map[string]string{
		"gt":                       "{field} must be positive",
		"gte":                      "{field} must not be negative",
		"min":                      fmt.Sprintf("{field} must be at least %s", minRetention),
		"LogLevel.enum":            "{field} must be debug, info, warn or error",
		"LogFormat.enum":           "{field} must be json or text",
		"enum":                     "{field} must be kubernetes or local",
		"required":                 "{field} must be set",
		"neField":                  "{field} must be distinct from databaseAppRole",
		"GatewayCacheStorage.enum": "{field} must be filesystem or s3",
	})
	labels := map[string]string{}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[Settings]()) {
		name := f.Tag.Get("env")
		if name == "" {
			continue
		}
		path, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if len(f.Index) > 1 {
			path = "runner." + path
		}
		labels[f.Name] = path + ": " + name
	}
	v.WithTranslates(labels)
	var extra []string
	if _, err := s.deadline(); err != nil {
		extra = append(extra, err.Error())
	}
	if u, err := url.Parse(s.AquaRegistryURL); err != nil || u.Host == "" || (u.Scheme != schemeHTTPS && u.Scheme != schemeHTTP) {
		extra = append(extra, "aquaRegistryUrl: KRITIKA_AQUA_REGISTRY_URL must be an http(s) URL")
	}
	if s.GatewayCacheStorage == "s3" {
		if s.GatewayCacheDir == "" {
			extra = append(extra, "gatewayCacheDir: S3 needs a private local staging directory")
		}
		if s.GatewayCacheMaxEntrySize > 5<<30 {
			extra = append(extra, "gatewayCacheMaxEntrySize: S3 single-request uploads must not exceed 5Gi")
		}
		if err := checkS3Bucket(s.GatewayCacheS3Endpoint, s.GatewayCacheS3Region, s.GatewayCacheS3Bucket); err != nil {
			extra = append(extra, err.Error())
		}
	}
	return validationError(v, extra...)
}

// checkS3Bucket makes sure that the location of the gateway cache's bucket
// is valid, without contacting the object store.
func checkS3Bucket(endpoint, region, bucket string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != schemeHTTPS && u.Scheme != schemeHTTP) ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("gatewayCacheS3Endpoint: the S3 endpoint must be an http(s) origin without credentials, path, query or fragment")
	}
	if region == "" {
		return errors.New("gatewayCacheS3Region: the S3 region must be set")
	}
	if err := s3utils.CheckValidBucketName(bucket); err != nil {
		return fmt.Errorf("gatewayCacheS3Bucket: S3 bucket: %w", err)
	}
	return nil
}

// ValidateServe makes sure that c has what serve needs beyond the common set.
func (c *Config) ValidateServe() error {
	v := validate.Struct(c)
	v.StopOnError = false
	v.SkipOnEmpty = false
	v.AddRule("ConfigFile,WebURL,GatewayURL", "required")
	// Executor is a named string type. requiredIf compares it to a plain
	// string.
	v.AddRule("RunnerDatabaseSecret,RunnerDatabaseSecretKey", "required").SetBeforeFunc(func(*validate.Validation) bool {
		return c.Executor == ExecutorKubernetes
	})
	v.AddRule("RunnerDatabaseURL", "required").SetBeforeFunc(func(*validate.Validation) bool {
		return c.Executor == ExecutorLocal
	})
	v.WithTranslates(map[string]string{
		"ConfigFile":              "KRITIKA_CONFIG_FILE",
		"WebURL":                  "webUrl: KRITIKA_WEB_URL",
		"GatewayURL":              "gatewayUrl: KRITIKA_GATEWAY_URL",
		"RunnerDatabaseSecret":    "runner.databaseSecret: KRITIKA_RUNNER_DATABASE_SECRET",
		"RunnerDatabaseSecretKey": "runner.databaseSecretKey: KRITIKA_RUNNER_DATABASE_SECRET_KEY",
		"RunnerDatabaseURL":       "KRITIKA_RUNNER_DATABASE_URL",
	})
	v.WithMessages(map[string]string{
		"required":                   "{field} is required to serve",
		"RunnerDatabaseURL.required": "{field} is required with the local executor",
	})
	if (c.GatewayCacheS3AccessKey == "") != (c.GatewayCacheS3SecretKey == "") {
		return validationError(v, "both KRITIKA_GATEWAY_CACHE_S3_ACCESS_KEY and KRITIKA_GATEWAY_CACHE_S3_SECRET_KEY must be set")
	}
	return validationError(v)
}

func validationError(v *validate.Validation, extra ...string) error {
	if v.Validate() && len(extra) == 0 {
		return nil
	}
	messages := slices.Clone(extra)
	for _, rules := range v.Errors {
		for _, message := range rules {
			messages = append(messages, message)
		}
	}
	slices.Sort(messages)
	return fmt.Errorf("config: %w", errors.New(strings.Join(messages, "; ")))
}
