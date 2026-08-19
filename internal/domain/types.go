package domain

import "time"

type Role string

const (
	RoleManager     Role = "manager"
	RoleOperator    Role = "operator"
	RoleEnvironment Role = "environment_officer"
)

type User struct {
	ID, TenantID, Email, PasswordDigest string
	Role                                Role
	Disabled                            bool
}
type Session struct {
	ID, UserID, TenantID, TokenDigest string
	ExpiresAt                         time.Time
	RevokedAt                         *time.Time
}
type Barn struct {
	ID, TenantID, Name string
	Capacity           int
	Status             string
	Version            int64
}
type AnimalGroup struct {
	ID, TenantID, BarnID, Name string
	Headcount                  int
	Status                     string
	Version                    int64
}
type FeedPlan struct {
	ID, TenantID, GroupID, OperatorID, Status string
	FeedKg                                    float64
	ScheduledFor                              time.Time
	CompletedAt                               *time.Time
	Version                                   int64
}
type FeedRound struct {
	ID, TenantID, PlanID, IdempotencyKey, Status string
	DeliveredKg                                  float64
	RecordedAt                                   time.Time
}
type ManureBatch struct {
	ID, TenantID, GroupID, SourceRoundID, Status string
	WeightKg                                     float64
	CollectedAt                                  time.Time
	Version                                      int64
}
type CompostLot struct {
	ID, TenantID, BatchID, Status string
	OutputKg                      float64
	ApprovedBy                    string
	Version                       int64
}
type AuditEvent struct {
	ID, TenantID, ActorID, Action, ObjectType, ObjectID, Outcome string
	At                                                           time.Time
}
type OutboxJob struct {
	ID, TenantID, Topic, Payload, Status string
	Attempts                             int
	AvailableAt                          time.Time
}
