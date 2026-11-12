import { MantineProvider, createTheme } from "@mantine/core";
import type { ReactNode } from "react";
import { I18nContext } from "./context";
import type { Locale, Messages } from "./i18n";

export const spectraTheme = createTheme({
  primaryColor: "forge",
  primaryShade: { light: 6, dark: 3 },
  colors: { forge: ["#f5f3ff", "#eeebff", "#dcd7ff", "#b7afff", "#a195fc", "#8374ef", "#675be5", "#574bd0", "#493fba", "#3c329f"] },
  fontFamily: '"Inter", system-ui, sans-serif',
  fontFamilyMonospace: '"JetBrains Mono", monospace',
  headings: { fontFamily: '"Inter", system-ui, sans-serif', fontWeight: "650" },
  defaultRadius: "md",
  focusRing: "auto",
  components: {
    Button: { defaultProps: { radius: "md", fw: 550 } },
    FileInput: { defaultProps: { size: "md" } },
    PasswordInput: { defaultProps: { size: "md" } },
    TextInput: { defaultProps: { size: "md", inputWrapperOrder: ["label", "input", "description", "error"] } },
    Select: { defaultProps: { size: "md", inputWrapperOrder: ["label", "input", "description", "error"] } },
    Textarea: { defaultProps: { size: "md" } },
  },
});

export function AppProviders({ theme, i18n, children }: {
  theme: string;
  i18n: { locale: Locale; setLocale: (value: Locale) => void; m: Messages };
  children: ReactNode;
}) {
  return <MantineProvider withCssVariables={false} withGlobalClasses={false} theme={spectraTheme} forceColorScheme={theme === "dark" ? "dark" : "light"}>
    <I18nContext.Provider value={i18n}>{children}</I18nContext.Provider>
  </MantineProvider>;
}
