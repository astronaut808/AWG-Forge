import { Button as MantineButton, UnstyledButton } from "@mantine/core";
import type { ButtonHTMLAttributes } from "react";

/** Shared action control; navigation keeps its own layout and theme tokens. */
export function Button({ className = "", children, type = "button", ...props }: ButtonHTMLAttributes<HTMLButtonElement>) {
  if (!className.split(" ").includes("button")) {
    return <UnstyledButton type={type} className={className} {...props}>{children}</UnstyledButton>;
  }
  const variant = className.includes("primary") ? "filled" : className.includes("text") ? "subtle" : "default";
  return <MantineButton type={type} className={className} variant={variant} color={className.includes("danger") ? "red" : undefined} {...props}>{children}</MantineButton>;
}
