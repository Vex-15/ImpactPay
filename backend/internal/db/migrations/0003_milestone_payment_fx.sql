-- Records the indicative currency conversion applied when a milestone release is
-- paid out. PayPal settles in the account currency while a project budgets in its
-- own, so the conversion is stored alongside the payment rather than inferred.

ALTER TABLE milestone_payments ADD COLUMN fx_note TEXT NOT NULL DEFAULT '{}';
