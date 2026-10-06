import Link from "next/link";

import { ProjectCard } from "@/components/project-card";
import { Alert } from "@/components/ui/misc";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader, CardTitle, Metric } from "@/components/ui/card";
import { SectionHeading } from "@/components/ui/misc";
import { serverGet } from "@/lib/api";
import { formatMoney, formatMoneyShort } from "@/lib/format";
import type { ProjectCard as ProjectCardType } from "@/lib/types";

export const dynamic = "force-dynamic";

const steps = [
  {
    title: "Requirement retrieved",
    body: "The agent loads the milestone definition: the quantity required, the unit, the budget and every declared evidence artefact.",
    tool: "get_milestone",
  },
  {
    title: "Evidence retrieved",
    body: "All artefacts for the milestone, cumulative across review rounds, are pulled from storage with their extracted facts.",
    tool: "get_evidence",
  },
  {
    title: "Facts extracted",
    body: "Images are decoded and captions read; invoices and registries are parsed for amounts, quantities and asset serial numbers.",
    tool: "extract_invoice_data",
  },
  {
    title: "Duplicates and coverage checked",
    body: "Artefacts are content-addressed, so a re-upload is an exact hash match. Required artefacts are counted against the declaration.",
    tool: "check_duplicate_evidence",
  },
  {
    title: "Quantities and money compared",
    body: "Distinct serial numbers are counted against the requirement, and documented spend is reconciled with the milestone budget.",
    tool: "compare_requirements",
  },
  {
    title: "A decision is recorded",
    body: "APPROVE, HOLD or REQUEST_MORE_EVIDENCE is stored with every check, finding and citation. Release still requires backend policy to pass.",
    tool: "calculate_verification",
  },
];

export default async function LandingPage() {
  let featured: ProjectCardType[] = [];
  let totals = { projects: 0, raised: 0, released: 0, verifiedMilestones: 0, currency: "INR" };
  let loadError = false;

  try {
    const res = await serverGet<{ projects: ProjectCardType[] }>("/api/projects?limit=3&sort=progress");
    featured = res.projects ?? [];
    for (const p of featured) {
      totals.projects += 1;
      totals.raised += p.funded_amount;
      totals.released += p.released_amount;
      totals.verifiedMilestones += p.progress.verified;
      totals.currency = p.currency || totals.currency;
    }
  } catch {
    loadError = true;
  }

  return (
    <div className="flex flex-col gap-14 pb-8">
      <section className="grid gap-8 pt-6 lg:grid-cols-[1.35fr_1fr]">
        <div className="flex flex-col gap-5">
          <span className="text-[11px] font-medium uppercase tracking-[0.1em] text-ink-faint">
            Outcome-based funding · evidence-verified
          </span>
          <h1 className="text-4xl font-semibold leading-[1.1] tracking-tight text-ink">
            Funding that is tied to what it accomplishes.
          </h1>
          <p className="max-w-2xl text-sm leading-relaxed text-ink-soft">
            Donors can already send money to an NGO. What they cannot do is verify what it achieved. ImpactPay
            splits every project into milestone budgets, requires specific evidence for each one, and runs a
            deterministic verification agent over that evidence before a single rupee is released.
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Link href="/projects">
              <Button size="lg">Browse funded outcomes</Button>
            </Link>
            <Link href="/register">
              <Button size="lg" variant="secondary">
                Publish a project
              </Button>
            </Link>
          </div>
          <p className="max-w-2xl text-xs leading-relaxed text-ink-faint">
            Every financial action in this prototype runs against the PayPal sandbox. The release engine
            re-validates eleven deterministic gates before any money moves, so an AI recommendation is never
            enough on its own.
          </p>
        </div>

        <Card className="self-start">
          <CardHeader>
            <CardTitle>How a milestone gets paid</CardTitle>
          </CardHeader>
          <CardBody className="flex flex-col gap-3 text-xs leading-relaxed text-ink-soft">
            <ol className="flex flex-col gap-2">
              {[
                "Project is created with a funding goal and milestone budgets.",
                "Donors fund it through PayPal; each contribution is attributed to milestones in order.",
                "The organization starts a milestone and submits evidence.",
                "The verification agent compares evidence with the requirement and recommends a decision.",
                "A blocked verdict holds the money. A blocked *policy gate* holds it too.",
                "Funding is released to the organization only when both agree.",
              ].map((line, index) => (
                <li key={line} className="flex gap-2.5">
                  <span className="tnum mt-0.5 grid h-4 w-4 shrink-0 place-items-center rounded-full bg-accent-soft text-[10px] font-semibold text-accent-strong">
                    {index + 1}
                  </span>
                  <span>{line}</span>
                </li>
              ))}
            </ol>
          </CardBody>
        </Card>
      </section>

      {loadError ? (
        <Alert tone="held" title="The API is not reachable">
          Start the backend (<code className="font-mono">go run ./cmd/server</code> with{" "}
          <code className="font-mono">DEMO_MODE=1</code>) and reload. This page renders live data from it.
        </Alert>
      ) : null}

      <section className="flex flex-col gap-4">
        <SectionHeading
          title="Verification, not vibes"
          description="The agent is a tool-using pipeline with six recorded stages. Every stage is deterministic, so the same stored evidence always produces the same verdict — including the demo's 87/100 hold."
        />
        <div className="grid gap-3 md:grid-cols-3">
          {steps.map((step, index) => (
            <Card key={step.tool}>
              <CardBody className="flex flex-col gap-1.5">
                <div className="flex items-center justify-between gap-2">
                  <span className="tnum text-[11px] font-semibold text-ink-faint">
                    Stage {index + 1}
                  </span>
                  <code className="rounded bg-subtle px-1.5 py-0.5 font-mono text-[10px] text-ink-soft">
                    {step.tool}()
                  </code>
                </div>
                <h3 className="text-sm font-semibold text-ink">{step.title}</h3>
                <p className="text-xs leading-relaxed text-ink-soft">{step.body}</p>
              </CardBody>
            </Card>
          ))}
        </div>
      </section>

      {featured.length > 0 ? (
        <section className="flex flex-col gap-4">
          <SectionHeading
            title="Live from the platform"
            description="Figures below are read straight from the database — captured funding, released funding and verified milestone counts."
            action={
              <Link href="/projects" className="text-xs font-medium text-accent hover:underline">
                See all projects →
              </Link>
            }
          />
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <Metric label="Projects shown" value={totals.projects} hint="on the marketplace" />
            <Metric
              label="Funding captured"
              value={formatMoneyShort(totals.raised, totals.currency)}
              hint={formatMoney(totals.raised, totals.currency)}
              tone="accent"
            />
            <Metric
              label="Released to organizations"
              value={formatMoneyShort(totals.released, totals.currency)}
              hint="after evidence verification"
              tone="verified"
            />
            <Metric
              label="Milestones verified"
              value={totals.verifiedMilestones}
              hint="requirement fully supported"
              tone="verified"
            />
          </div>
          <div className="grid gap-3 md:grid-cols-3">
            {featured.map((project) => (
              <ProjectCard key={project.id} project={project} />
            ))}
          </div>
        </section>
      ) : null}

      <section className="rounded-[var(--radius-card)] border border-line bg-surface px-8 py-10 text-center">
        <p className="text-lg font-medium leading-relaxed tracking-tight text-ink">
          ImpactPay doesn&apos;t just show where money went.
          <br />
          It shows what the money accomplished.
        </p>
      </section>
    </div>
  );
}
