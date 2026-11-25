import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { cn } from "@/lib/utils"

const badgeStyles = cva(
  "inline-flex items-center gap-1 rounded px-2 py-0.5 text-[11px] font-medium leading-tight border",
  {
    variants: {
      variant: {
        default: "bg-secondary text-secondary-foreground border-border",
        mono: "font-mono bg-muted text-muted-foreground border-border",
        ok: "bg-emerald-500/12 text-emerald-700 dark:text-emerald-300 border-emerald-500/30",
        info: "bg-blue-500/12 text-blue-700 dark:text-blue-300 border-blue-500/30",
        warn: "bg-amber-500/15 text-amber-800 dark:text-amber-300 border-amber-500/30",
        err: "bg-red-500/12 text-red-700 dark:text-red-300 border-red-500/30",
        muted: "bg-muted text-muted-foreground border-border/80",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeStyles> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeStyles({ variant }), className)} {...props} />
}
