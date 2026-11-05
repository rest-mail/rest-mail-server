package mail

// GreylistedRefusal is the error code of the deliver route's 451 when the inbound
// pipeline's greylist step held the message back. The gateway turns it into the
// SMTP reply that says so.
const GreylistedRefusal = "greylisted"
