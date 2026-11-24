import type { ClassValue } from 'clsx'
import { clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

/** Fusionne des classes Tailwind en résolvant les conflits (utilitaire standard shadcn-vue). */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
