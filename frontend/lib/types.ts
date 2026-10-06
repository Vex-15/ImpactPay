// Types mirroring the ImpactPay API responses.
//
// Every figure that represents money is an integer in *minor units* (paise or
// cents) exactly as the backend stores it. Nothing is converted to a float on
// the way in, and formatting happens in one place only (lib/format.ts).

export type Role = "donor" | "organization" | "admin";

export type ProjectStatus =
  | "DRAFT"
  | "FUNDING_OPEN"
  | "IN_PROGRESS"
  | "COMPLETED"
  | "ARCHIVED"
  | "SUSPENDED";

export type MilestoneStatus =
  | "PENDING"
  | "IN_PROGRESS"
  | "EVIDENCE_SUBMITTED"
  | "VERIFYING"
  | "APPROVED"
  | "HELD"
  | "MORE_EVIDENCE_REQUESTED"
  | "PAYMENT_RELEASED"
  | "COMPLETED"
  | "REFUNDED";

export type Decision = "APPROVE" | "HOLD" | "REQUEST_MORE_EVIDENCE";

export type DonationStatus =
  | "PENDING"
  | "ORDER_CREATED"
  | "CAPTURED"
  | "FAILED"
  | "CANCELLED"
  | "REFUNDED"
  | "RELEASED";

export interface User {
  id: string;
  email: string;
  full_name: string;
  role: Role;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface Organization {
  id: string;
  user_id: string;
  name: string;
  slug: string;
  mission: string;
  description: string;
  website: string;
  country: string;
  registration_number: string;
  contact_email: string;
  payout_email: string;
  verification_status: "PENDING" | "VERIFIED" | "REJECTED" | "SUSPENDED";
  verified: boolean;
  review_note: string;
  reviewed_at?: string;
  created_at: string;
  updated_at: string;
}

export interface OrgBrief {
  id: string;
  name: string;
  slug: string;
  verified: boolean;
  verification_status: string;
  country: string;
}

export interface EvidenceRequirement {
  key: string;
  label: string;
  kind: string;
  required: boolean;
  min_count: number;
  detail?: string;
}

export interface Project {
  id: string;
  org_id: string;
  title: string;
  slug: string;
  category: string;
  summary: string;
  problem: string;
  approach: string;
  location: string;
  currency: string;
  goal_amount: number;
  funded_amount: number;
  reserved_amount: number;
  released_amount: number;
  beneficiary_count: number;
  beneficiary_description: string;
  outcome_description: string;
  status: ProjectStatus;
  cover_seed: string;
  created_by: string;
  created_at: string;
  updated_at: string;
  published_at?: string;
  completed_at?: string;
}

export interface ProjectProgress {
  total: number;
  completed: number;
  verified: number;
  in_progress: number;
  held: number;
  approved: number;
  released: number;
}

// The API embeds the project's fields at the top level of a marketplace card and
// decorates them with the organization header and verified milestone progress.
export interface ProjectCard {
  id: string;
  org_id: string;
  title: string;
  slug: string;
  category: string;
  summary: string;
  location: string;
  currency: string;
  goal_amount: number;
  funded_amount: number;
  reserved_amount: number;
  released_amount: number;
  beneficiary_count: number;
  beneficiary_description: string;
  outcome_description: string;
  status: ProjectStatus;
  organization: OrgBrief;
  progress: ProjectProgress;
  funding_percent: number;
  verified_percent: number;
}

export interface Milestone {
  id: string;
  project_id: string;
  ordinal: number;
  title: string;
  description: string;
  requirement: string;
  unit_label: string;
  required_quantity: number;
  budget_amount: number;
  evidence_requirements: EvidenceRequirement[];
  status: MilestoneStatus;
  verified_quantity: number;
  created_at: string;
  updated_at: string;
  started_at?: string;
  submitted_at?: string;
  decided_at?: string;
  completed_at?: string;
}

export interface Claim {
  label: string;
  quantity: number;
  unit: string;
  source: string;
}

export interface EvidenceFinding {
  evidence_id: string;
  filename: string;
  kind: string;
  requirement_key?: string;
  summary: string;
  unit_ids?: string[];
  amount_minor?: number;
  usable: boolean;
  confidence: number;
  duplicate_of?: string;
  issue?: string;
}

export interface Inconsistency {
  code: string;
  severity: "info" | "warning" | "critical";
  detail: string;
  evidence_id?: string;
}

export interface MissingEvidence {
  requirement_key: string;
  label: string;
  kind: string;
  required_count: number;
  found_count: number;
  detail: string;
}

export interface VerificationCheck {
  key: string;
  label: string;
  status: "pass" | "warn" | "fail";
  detail: string;
  weight: number;
  observed?: number;
  expected?: number;
  unit?: string;
}

export interface FinancialCheck {
  budget_minor: number;
  documented_minor: number;
  variance_minor: number;
  variance_percent: number;
  documents_analysed: number;
  currency: string;
  status: "ok" | "overspend" | "undocumented" | "mismatch" | "not_applicable";
  notes: string[];
}

export interface VerificationStep {
  index: number;
  tool: string;
  title: string;
  input: string;
  output: string;
  status: "ok" | "warn" | "fail";
  duration_ms: number;
}

export interface VerificationSummary {
  id: string;
  status: Decision;
  recommendation: Decision;
  confidence: number;
  verified_quantity: number;
  required_quantity: number;
  unit_label: string;
  summary: string;
  engine: string;
  model: string;
  is_override: boolean;
  human_decision?: string;
  human_note?: string;
  created_at: string;
  decided_at?: string;
  claimed: Claim[];
  evidence_found: EvidenceFinding[];
  inconsistencies: Inconsistency[];
  missing_evidence: MissingEvidence[];
  checks: VerificationCheck[];
  financial: FinancialCheck;
  verified_unit_ids: string[];
  evidence_count: number;
  usable_count: number;
  duplicate_count: number;
  step_count: number;
}

export interface EvidenceItem {
  id: string;
  submission_id: string;
  milestone_id: string;
  project_id: string;
  uploaded_by: string;
  kind: string;
  requirement_key: string;
  filename: string;
  mime_type: string;
  size_bytes: number;
  content_hash: string;
  description: string;
  declared: Record<string, unknown>;
  extracted: {
    kind?: string;
    vendor?: string;
    invoice_number?: string;
    document_date?: string;
    amount_minor?: number;
    stated_quantity?: number;
    unit_ids?: string[];
    beneficiaries?: number;
    villages?: string[];
    location?: string;
    width?: number;
    height?: number;
    quality?: string;
    notes?: string[];
    parse_errors?: string[];
  };
  created_at: string;
}

export interface EvidenceSubmission {
  id: string;
  milestone_id: string;
  project_id: string;
  submitted_by: string;
  sequence: number;
  note: string;
  status: string;
  verification_id?: string;
  created_at: string;
}

export interface CoverageRow {
  key: string;
  label: string;
  kind: string;
  required: boolean;
  min_count: number;
  found: number;
  satisfied: boolean;
}

export interface MilestoneView extends Milestone {
  project?: ProjectBrief;
  organization?: OrgBrief;
  latest_verification?: VerificationSummary;
  evidence_coverage?: CoverageRow[];
  evidence_count: number;
  duplicate_count: number;
  submission_count: number;
  allocated_minor: number;
  verified_percent: number;
}

export interface ProjectBrief {
  id: string;
  title: string;
  slug: string;
  category: string;
  status: ProjectStatus;
  currency: string;
  goal_amount: number;
  funded_amount: number;
  released_amount: number;
}

export interface Donation {
  id: string;
  project_id: string;
  donor_id: string;
  amount: number;
  currency: string;
  status: DonationStatus;
  paypal_order_id: string;
  paypal_capture_id: string;
  capture_status: string;
  provider: string;
  simulated: boolean;
  note: string;
  created_at: string;
  updated_at: string;
  captured_at?: string;
  refunded_at?: string;
}

export interface FundingView {
  milestone_id: string;
  ordinal: number;
  title: string;
  unit_label: string;
  status: MilestoneStatus;
  amount_minor: number;
  currency: string;
  required_quantity: number;
  verified_quantity: number;
  verified_percent: number;
  latest_verification?: VerificationSummary;
}

export interface DonationView extends Donation {
  amount_view: MoneyView;
  project?: ProjectBrief;
  organization?: OrgBrief;
  fundings: FundingView[];
  verified_minor: number;
  pending_minor: number;
}

export interface MoneyView {
  minor: number;
  currency: string;
  label: string;
  short: string;
}

export interface PaymentEvent {
  id: string;
  donation_id: string;
  project_id: string;
  milestone_id: string;
  type: string;
  provider: string;
  provider_ref: string;
  amount: number;
  currency: string;
  status: string;
  detail: string;
  actor_id: string;
  simulated: boolean;
  created_at: string;
}

export interface PolicyCheck {
  key: string;
  label: string;
  passed: boolean;
  detail: string;
}

export interface FXNote {
  from_currency: string;
  to_currency: string;
  rate: number;
  local_minor: number;
  account_minor: number;
  source: string;
}

export interface MilestonePayment {
  id: string;
  milestone_id: string;
  project_id: string;
  org_id: string;
  donation_id?: string;
  kind: "RELEASE" | "REFUND" | "PAYOUT";
  amount: number;
  currency: string;
  status: "PENDING" | "EXECUTED" | "BLOCKED" | "FAILED" | "SIMULATED";
  provider: string;
  paypal_ref: string;
  paypal_status: string;
  policy_checks: PolicyCheck[];
  fx: FXNote;
  blocked_reason: string;
  executed_by: string;
  simulated: boolean;
  created_at: string;
  executed_at?: string;
}

export interface AuditEvent {
  id: string;
  actor_id: string;
  actor_role: string;
  actor_label: string;
  action: string;
  entity_type: string;
  entity_id: string;
  project_id: string;
  milestone_id: string;
  donation_id: string;
  severity: "info" | "warning" | "critical";
  summary: string;
  created_at: string;
}

export interface Notification {
  id: string;
  user_id: string;
  type: string;
  title: string;
  body: string;
  link: string;
  read: boolean;
  created_at: string;
}

export interface ProjectUpdate {
  id: string;
  project_id: string;
  author_id: string;
  title: string;
  body: string;
  created_at: string;
}

export interface FundingHistoryRow {
  donation: Donation;
  donor_name: string;
  donor_email: string;
}

export interface MilestoneImpact {
  id: string;
  ordinal: number;
  title: string;
  unit_label: string;
  status: MilestoneStatus;
  required_quantity: number;
  verified_quantity: number;
  verified_percent: number;
  budget_minor: number;
  allocated_minor: number;
  evidence_count: number;
  cost_per_unit_minor: number;
}

// --- Proof Graph -----------------------------------------------------------

export type NodeType =
  | "DONATION"
  | "PROJECT"
  | "PROJECT_FUND"
  | "MILESTONE"
  | "RESOURCE"
  | "EXPENSE"
  | "EVIDENCE"
  | "VERIFICATION"
  | "OUTCOME"
  | "BENEFICIARY";

export interface GraphMetric {
  label: string;
  value: string;
  unit?: string;
  raw?: number;
}

export interface EvidenceRef {
  id: string;
  filename: string;
  kind: string;
  description: string;
  size_bytes: number;
  mime_type: string;
  unit_ids?: string[];
  amount_minor?: number;
  vendor?: string;
  invoice_number?: string;
  location?: string;
  quality?: string;
  created_at: string;
  url: string;
}

export interface GraphNode {
  id: string;
  type: NodeType;
  title: string;
  subtitle: string;
  status: string;
  verified: boolean;
  amount_minor: number;
  currency: string;
  metrics: GraphMetric[];
  evidence: EvidenceRef[];
  detail: string;
}

export interface GraphEdge {
  from: string;
  to: string;
  label: string;
}

export interface ShareBreakdown {
  total_minor: number;
  verified_minor: number;
  pending_minor: number;
  held_minor: number;
  currency: string;
}

export interface ProofGraph {
  donation: Donation;
  project: Project;
  organization: OrgBrief;
  nodes: GraphNode[];
  edges: GraphEdge[];
  summary: string;
  verified_share: ShareBreakdown;
  allocation_note: string;
}

export interface Citation {
  label: string;
  detail: string;
  evidence_id?: string;
  node_id?: string;
  kind: string;
}

export interface Answer {
  question: string;
  answer: string;
  citations: Citation[];
  grounded: boolean;
  engine: string;
  model: string;
  facts: Record<string, unknown>;
  suggested_questions: string[];
}

// --- Impact Passport -------------------------------------------------------

export interface PassportProject {
  id: string;
  title: string;
  slug: string;
  category: string;
  status: ProjectStatus;
  goal_amount: number;
  funded_amount: number;
  released_amount: number;
  beneficiary_count: number;
  milestones_total: number;
  milestones_verified: number;
  evidence_completeness: number;
  unresolved_findings: number;
  currency: string;
}

export interface Passport {
  organization: {
    id: string;
    name: string;
    slug: string;
    mission: string;
    description: string;
    country: string;
    website: string;
    verification_status: string;
    verified: boolean;
    review_note: string;
    member_since: string;
  };
  projects_total: number;
  projects_completed: number;
  projects_active: number;
  funds_raised_minor: number;
  currency: string;
  milestones_total: number;
  milestones_completed: number;
  milestones_verified: number;
  milestones_held: number;
  evidence_completeness: number;
  evidence_required: number;
  evidence_supplied: number;
  duplicate_artefacts: number;
  contributions_count: number;
  supporter_count: number;
  unresolved_findings: number;
  verification_mix: Record<string, number>;
  projects: PassportProject[];
  average_confidence: number;
}

// --- Admin -----------------------------------------------------------------

export interface QueueItem {
  verification: VerificationSummary & { result: unknown };
  milestone: Milestone;
  project: Project;
  org_name: string;
}
