import * as React from "react"
import { cn } from "@/lib/utils"

export function Table({ className, ...props }: React.TableHTMLAttributes<HTMLTableElement>) {
  return (
    <div className="w-full overflow-x-auto">
      <table className={cn("w-full border-collapse text-xs", className)} {...props} />
    </div>
  )
}

export function TableHeader({
  className,
  ...props
}: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <thead className={cn("bg-muted/40 border-b", className)} {...props} />
}

export function TableBody({
  className,
  ...props
}: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody className={cn("divide-y divide-border", className)} {...props} />
}

export function TableRow({ className, ...props }: React.HTMLAttributes<HTMLTableRowElement>) {
  return (
    <tr className={cn("transition-colors hover:bg-muted/30", className)} {...props} />
  )
}

export function TableHead({
  className,
  ...props
}: React.ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th
      className={cn(
        "px-4 py-2.5 text-left text-[11px] font-medium text-muted-foreground whitespace-nowrap",
        className
      )}
      {...props}
    />
  )
}

export function TableCell({
  className,
  ...props
}: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={cn("px-4 py-3 align-middle", className)} {...props} />
}

export function AlertBox({
  variant = "info",
  className,
  children,
}: {
  variant?: "info" | "warn" | "err"
  className?: string
  children: React.ReactNode
}) {
  const styles = {
    info: "border-blue-500/30 bg-blue-500/8 text-blue-900 dark:text-blue-200",
    warn: "border-amber-500/35 bg-amber-500/10 text-amber-900 dark:text-amber-200",
    err: "border-red-500/35 bg-red-500/10 text-red-900 dark:text-red-200",
  }[variant]
  return (
    <div className={cn("rounded-lg border px-4 py-3 text-xs leading-relaxed", styles, className)}>
      {children}
    </div>
  )
}
