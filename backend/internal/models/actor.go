package models

import "context"

// Actor is the identity performing an action. It is carried in the request
// context so that services can attribute audit events without taking a
// dependency on the HTTP layer.
type Actor struct {
	ID    string `json:"id"`
	Role  Role   `json:"role"`
	Label string `json:"label"`
}

// SystemActor is used for work performed by the platform itself (seeders,
// background verification, webhook processing).
func SystemActor() Actor {
	return Actor{ID: "", Role: "system", Label: "system"}
}

// AuditRole returns the role string persisted on audit rows.
func (a Actor) AuditRole() string {
	if a.Role == "" {
		return "system"
	}
	return string(a.Role)
}

// AuditLabel returns a human-readable actor label.
func (a Actor) AuditLabel() string {
	if a.Label != "" {
		return a.Label
	}
	if a.ID == "" {
		return "system"
	}
	return a.ID
}

type actorKey struct{}

// WithActor stores the actor on the context.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor on the context, or the system actor.
func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return SystemActor()
}
