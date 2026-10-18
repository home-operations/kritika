# Confidence and approvals

These features are optional and off by default:

- **Confidence scoring** asks a second model how ready a pull request is to merge.
- **Gating** fails the commit status when the score is too low.
- **Automatic approval** posts a GitHub approval when the review meets your policy.

Scoring can run without gating or approvals. Approvals can also work without
a score. This example enables all three: it requires at least 4 out of 5 and
approves changes rated at most medium risk:

```yaml
review:
  approve: true
confidence:
  model: openrouter/other-vendor/model
  threshold: 4
  gate: true
  risk: medium
```

Use a model from a provider you configured. These settings belong under Helm's
`config`, or at the root of the application configuration file.

## The score

The scorer reads the pull request description, diff, findings and review summary.
It returns a score from **0 to 5** and a reason. A model from a different vendor
can provide a second opinion.

- A **P0** finding caps the score at **2**.
- A **P1** finding caps it at **3**.
- **P2** findings do not reduce it by themselves.
- Dismissed findings stop counting at the next review.

The scorer can raise its own evidence-backed concerns. Merge impact notes and
post-deployment checks do not lower the score by themselves.

Scoring uses tokens and adds cost. It has no tools and sees only the supplied
prompt. If retries and `confidence.fallback` fail, the review remains unscored.
The scorer gets two minutes, including time waiting for a model slot.

## The commit status

| Configuration | Result after a completed review |
| --- | --- |
| No confidence model | Success, regardless of findings. |
| Scoring enabled, gate off | Shows the score and passes. |
| Scoring enabled, gate on | Fails below the threshold; errors if no score is available. |

To block merging, also make **Kritika / Review** a required check in GitHub.
Risk does not affect this check. A high-risk change can pass its score gate
without receiving an automatic approval.

## Approvals

Set `review.approve: true` to enable approvals.

| Configuration | Approves when… |
| --- | --- |
| No confidence model | There are no P0 or P1 findings. |
| Confidence model set | The score meets `confidence.threshold` and risk is at most `confidence.risk`. |

An unscored review does not approve when a confidence model is configured.
A later review that no longer qualifies withdraws kritika's approval, as does
a reviewer requesting changes. Reviews of merged or closed pull requests
neither approve nor withdraw an approval.

The summary says **Approved** or **Not approved**, with a reason. A repository's
`.kritika.yaml` can turn approvals on or off.

## Risk

Risk describes the damage a change could cause if something was missed.
It controls automatic approval, independently of the confidence score.

| Risk | Typical impact |
| --- | --- |
| `low` | Documentation, tests, formatting or small changes that leave behavior alone. |
| `medium` | Routine application changes that a revert can undo. |
| `high` | Breaking changes to something other systems depend on, or expanded access. |
| `critical` | Data loss, exposed secrets, administrative access or outages a revert cannot quickly fix. |

Dependency updates are rated by both the version step and the dependency's role.
Major changes and widely shared dependencies generally carry more risk. Breaking
release notes can raise that rating. Updates below 1.0 and unversioned tags need
extra care; being a bot-generated update does not reduce risk.

Admins can add local guidance with `confidence.instructions`, for example:

```yaml
confidence:
  instructions: anything under db/migrations is critical
```

Repository settings cannot change these instructions or raise the allowed risk.
A later push normally keeps the earlier risk rating unless the change's impact
changes. Manual re-runs, re-runs of the same commit, and changes to scoring
instructions trigger a fresh rating.

## Settings reference

| Key                       | Default                 | What                                                                                       | From a `.kritika.yaml`                     |
| ------------------------- | ----------------------- | ------------------------------------------------------------------------------------------ | ------------------------------------------ |
| `confidence.model`        | none: nothing is scored | the model that scores, a `<provider>/<model>` held to the same providers as `review.model` | replaces                                   |
| `confidence.fallback`     | none                    | the model that takes the scorer's call when the confidence model fails ([fallback](models.md#fallback)) | replaces                                   |
| `confidence.effort`       | the provider's          | how hard the scorer, and the fallback that takes its call, reasons ([effort](models.md#effort)) | replaces                                   |
| `confidence.threshold`    | 5                       | the score approvals need, and a gated commit status                                        | replaces, either way                       |
| `confidence.gate`         | off                     | `true` fails the commit status under the threshold, so it can be a required check          | replaces, either way                       |
| `confidence.risk`         | `low`                   | the highest [risk](#risk) a change may be rated and still be approved                      | may only lower it; a higher one is dropped |
| `confidence.instructions` |                         | the admin's guidance to the scorer on [rating risk](#risk) in your code                    | the admin's alone                          |
