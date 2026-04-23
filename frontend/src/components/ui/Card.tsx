import type { PropsWithChildren } from "react";
import { cn } from "@/utils/cn";

interface CardProps {
  className?: string;
}

export default function Card({ children, className }: PropsWithChildren<CardProps>) {
  return <section className={cn("panel p-6", className)}>{children}</section>;
}
