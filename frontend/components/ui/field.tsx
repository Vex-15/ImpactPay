import * as React from "react";

import { cn } from "@/lib/utils";

const controlBase =
  "w-full rounded-md border border-line-strong bg-surface text-sm text-ink placeholder:text-ink-faint focus-visible:border-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/20 disabled:opacity-50";

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  ({ className, ...props }, ref) => (
    <input ref={ref} className={cn(controlBase, "h-9 px-3", className)} {...props} />
  ),
);
Input.displayName = "Input";

export const Textarea = React.forwardRef<
  HTMLTextAreaElement,
  React.TextareaHTMLAttributes<HTMLTextAreaElement>
>(({ className, ...props }, ref) => (
  <textarea ref={ref} className={cn(controlBase, "px-3 py-2", className)} {...props} />
));
Textarea.displayName = "Textarea";

export const Select = React.forwardRef<
  HTMLSelectElement,
  React.SelectHTMLAttributes<HTMLSelectElement>
>(({ className, children, ...props }, ref) => (
  <select ref={ref} className={cn(controlBase, "h-9 px-2", className)} {...props}>
    {children}
  </select>
));
Select.displayName = "Select";

/**
 * Field pairs a label with its control so no screen has to repeat the layout.
 * `hint` is for units and ranges; `error` is for validation feedback.
 */
export function Field({
  label,
  hint,
  error,
  htmlFor,
  children,
  className,
}: {
  label: string;
  hint?: string;
  error?: string;
  htmlFor?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <div className="flex items-baseline justify-between gap-3">
        <label htmlFor={htmlFor} className="text-xs font-medium text-ink">
          {label}
        </label>
        {hint ? <span className="text-[11px] text-ink-faint">{hint}</span> : null}
      </div>
      {children}
      {error ? <p className="text-[11px] text-critical">{error}</p> : null}
    </div>
  );
}

/** A read-only key/value row used throughout the evidence and policy panels. */
export function DetailRow({
  label,
  children,
  className,
}: {
  label: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex items-start justify-between gap-4 py-1.5", className)}>
      <dt className="text-xs text-ink-faint">{label}</dt>
      <dd className="tnum text-right text-xs font-medium text-ink">{children}</dd>
    </div>
  );
}
