import { createContext, useContext } from "react";
import type { Locale, Messages } from "./i18n";
import { messages } from "./i18n";
export const I18nContext = createContext<{ locale: Locale; setLocale: (locale: Locale) => void; m: Messages }>({ locale: "en", setLocale: () => {}, m: messages.en });
export function useI18n() { return useContext(I18nContext); }
