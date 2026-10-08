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
