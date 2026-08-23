package domain

// Outbox delivery lifecycle (ADR-0011, extended by ADR-0015).
//
// The dispatcher drives pending → leased → delivered/pending/dead; operators
// act on dead rows through requeue (dead → pending) and discard
// (dead → discarded). The machine is declared here like every other state
// machine so adding a state without wiring its edges fails the transition
// table consistency test.
type OutboxDeliveryStatus string

const (
	OutboxPending   OutboxDeliveryStatus = "pending"
	OutboxLeased    OutboxDeliveryStatus = "leased"
	OutboxDelivered OutboxDeliveryStatus = "delivered"
	OutboxDead      OutboxDeliveryStatus = "dead"
	OutboxDiscarded OutboxDeliveryStatus = "discarded"
)

var AllOutboxDeliveryStatuses = []OutboxDeliveryStatus{
	OutboxPending,
	OutboxLeased,
	OutboxDelivered,
	OutboxDead,
	OutboxDiscarded,
}

// outboxDeliveryTransitions encodes the closed delivery machine. Expired
// leases stay `leased` — reclaimability is a scheduling property measured by
// the store clock, not a status change. dead is the only state an operator
// may move: requeue restarts a bounded lifecycle, discard terminates the row
// explicitly without deleting it.
var outboxDeliveryTransitions = transitionTable[OutboxDeliveryStatus]{
	OutboxPending:   {OutboxLeased},
	OutboxLeased:    {OutboxDelivered, OutboxPending, OutboxDead},
	OutboxDelivered: {},
	OutboxDead:      {OutboxPending, OutboxDiscarded},
	OutboxDiscarded: {},
}

func CanTransitionOutboxDelivery(from, to OutboxDeliveryStatus) bool {
	return outboxDeliveryTransitions.allows(from, to)
}
