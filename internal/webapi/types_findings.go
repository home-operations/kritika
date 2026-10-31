package webapi

import "time"

// The shapes of the findings listed across an account, which
// routes_findings.go serves.

// AccountFinding is one finding of a pull request, however many of its
// reviews reported it, as the latest of them did.
type AccountFinding struct {
	Finding
	ReviewID    string    `json:"reviewId"`
	Pull        PullRef   `json:"pull"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
}

// FindingCounts is how many findings there are, and how many of those were
// addressed, dismissed, or drew a 👎 on their comment.
type FindingCounts struct {
	Findings  int `json:"findings"`
	Addressed int `json:"addressed"`
	Dismissed int `json:"dismissed"`
	Downvoted int `json:"downvoted"`
}

// FindingGroup is the findings that share a key, counted. The key is a
// category, a rule id, an agent's name, "default" for the default pass, or a
// directory of Repository. A directory key is one below the path filter, with
// a trailing slash. For a file in no directory below the path filter, the key
// is the path filter itself ("" for none).
type FindingGroup struct {
	Key        string `json:"key"`
	Repository string `json:"repository"`
	FindingCounts
}

// FindingSummary is the findings the list's filters keep, counted, and the
// most common categories, rules, agents and directories among them, the
// most common first.
type FindingSummary struct {
	FindingCounts
	Categories  []FindingGroup `json:"categories"`
	Rules       []FindingGroup `json:"rules"`
	Agents      []FindingGroup `json:"agents"`
	Directories []FindingGroup `json:"directories"`
}

// FindingExport is the findings that the list's filters keep, newest first,
// as an export serves them as JSON. Truncated says that the filters keep more
// than it holds.
type FindingExport struct {
	Items     []AccountFinding `json:"items"`
	Truncated bool             `json:"truncated"`
}
