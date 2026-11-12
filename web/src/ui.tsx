import { Button as MantineButton, Select, UnstyledButton } from "@mantine/core";
import type { SelectProps } from "@mantine/core";
import { createContext, useContext, useId } from "react";
import type { ButtonHTMLAttributes, MouseEventHandler, ReactNode } from "react";
import { Icon } from "./icons";

// A popup belongs to the same focus/overlay layer as its field, outside the scrolling body.
export const PopupLayerContext = createContext<HTMLElement | null>(null);

export function SelectField({ className = "", onKeyDown, ...props }: SelectProps) {
  const layer = useContext(PopupLayerContext);
  return <Select {...props} className={`form-control ${className}`} allowDeselect={false} withScrollArea={false}
    rightSection={<span className="select-chevron"><Icon name="chevron" /></span>}
    onKeyDown={(event) => {
      onKeyDown?.(event);
      // Escape dismisses the list first; the parent dialog handles the next press.
      if (event.key === "Escape" && event.currentTarget.getAttribute("aria-expanded") === "true") event.stopPropagation();
    }}
    checkIconPosition="right" classNames={{ dropdown: "select-menu", option: "select-option" }}
    comboboxProps={{ withinPortal: true, portalProps: { target: layer || undefined }, floatingStrategy: "fixed", position: "bottom-start", width: "target", offset: 6, middlewares: { flip: true, shift: { padding: 12 } } }} />;
}

export function ChoiceGroup({ label, value, onChange, options }: { label: string; value: string; onChange: (value: string) => void; options: { value: string; label: ReactNode }[] }) {
  const name = useId();
  return <fieldset className="choice-group"><legend>{label}</legend><div className="choice-options">{options.map((option) => <label className="choice-option" key={option.value}>
    <input type="radio" name={name} value={option.value} checked={value === option.value} onChange={() => onChange(option.value)} />
    <span>{option.label}</span>
  </label>)}</div></fieldset>;
}

/** Shared action control; navigation keeps its own layout and theme tokens. */
export function Button({ className = "", children, type = "button", onClick, ...props }: ButtonHTMLAttributes<HTMLButtonElement>) {
  const activate: MouseEventHandler<HTMLButtonElement> = (event) => {
    // Safari pointer activation does not focus buttons; overlays need a reliable opener.
    event.currentTarget.focus({ preventScroll: true });
    onClick?.(event);
  };
  if (!className.split(" ").includes("button")) {
    return <UnstyledButton type={type} className={className} onClick={activate} {...props}>{children}</UnstyledButton>;
  }
  const variant = className.includes("primary") ? "filled" : className.includes("text") ? "subtle" : "default";
  return <MantineButton type={type} className={className} variant={variant} color={className.includes("danger") ? "red" : undefined} onClick={activate} {...props}>{children}</MantineButton>;
}
