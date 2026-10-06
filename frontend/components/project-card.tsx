import Link from "next/link";

import { Money } from "@/components/money";
import { Badge, OrgVerifiedBadge } from "@/components/ui/badge";
import { FundingBar } from "@/components/ui/progress";
import { humanize } from "@/lib/format";
import type { ProjectCard as ProjectCardType } from "@/lib/types";

/**
 * A marketplace card states only verifiable facts: how much has been captured,
 * how much of *that* has been released against verified milestones, and how many
 * milestones have been verified. Nothing is inferred or projected.
 */
export function ProjectCard({ project }: { project: ProjectCardType }) {
  const currency = project.currency;
  const verified = project.progress.verified;
  const total = project.progress.total;

  return (
    <Link
      href={`/projects/${project.slug}`}
      className="group flex flex-col gap-3 rounded-[var(--radius-card)] border border-line bg-surface p-5 transition-colors hover:border-line-strong"
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <span className="text-[11px] font-medium uppercase tracking-wide text-ink-faint">
            {project.category || "General"}
          </span>
          <h3 className="text-sm font-semibold leading-snug tracking-tight text-ink group-hover:text-accent-strong">
            {project.title}
          </h3>
          <span className="text-xs text-ink-soft">{project.organization?.name ?? "Unknown organization"}</span>
        </div>
        <OrgVerifiedBadge verified={project.organization?.verified ?? false} />
      </div>

      <p className="line-clamp-2 text-xs leading-relaxed text-ink-soft">{project.summary}</p>

      <div className="flex flex-col gap-1.5">
        <div className="flex items-baseline justify-between gap-3">
          <span className="tnum text-sm font-semibold text-ink">
            <Money minor={project.funded_amount} currency={currency} short />
            <span className="text-ink-faint"> / </span>
            <Money minor={project.goal_amount} currency={currency} short />
            <span className="ml-1 text-xs font-normal text-ink-faint">funded</span>
          </span>
          <span className="tnum text-[11px] text-ink-faint">
            <Money minor={project.released_amount} currency={currency} short /> released
          </span>
        </div>
        <FundingBar funded={project.funded_amount} goal={project.goal_amount} released={project.released_amount} />
      </div>

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-ink-faint">
        <span className="tnum">{project.beneficiary_count.toLocaleString("en-IN")} beneficiaries</span>
        <span aria-hidden>·</span>
        <span className="tnum">
          {verified}/{total} milestones verified
        </span>
        {project.location ? (
          <>
            <span aria-hidden>·</span>
            <span>{project.location}</span>
          </>
        ) : null}
      </div>

      {project.status !== "FUNDING_OPEN" && project.status !== "IN_PROGRESS" ? (
        <Badge tone="neutral">{humanize(project.status)}</Badge>
      ) : null}
    </Link>
  );
}
