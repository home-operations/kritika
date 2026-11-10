package worker

import (
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
)

// gatewayRequestTimeout bounds the runner's request to the gateway for a
// step of a run granted models and reviewers: the longest budget of a step
// on models or on one of the reviewers' (configfile.File.StepBudget), and
// the time the gateway takes to record the answer.
func gatewayRequestTimeout(
	file *configfile.File, account *configfile.Account, models configfile.Models, reviewers map[string]store.ReviewerGrant,
) time.Duration {
	budget := file.StepBudget(account, models.Review, models.Fallback)
	for _, r := range reviewers {
		budget = max(budget, file.StepBudget(account, configfile.ModelRef(r.Model), configfile.ModelRef(r.Fallback)))
	}
	return model.GatewayRequestTimeout(budget)
}
