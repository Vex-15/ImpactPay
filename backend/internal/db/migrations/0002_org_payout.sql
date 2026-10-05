-- The PayPal sandbox account that receives released milestone funding.
-- Kept separate from contact_email because the payout receiver is a payment
-- destination, not a correspondence address, and the two are set by different
-- people in a real organization.

ALTER TABLE organizations ADD COLUMN payout_email TEXT NOT NULL DEFAULT '';
