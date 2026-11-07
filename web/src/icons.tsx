import {
  IconHome, IconHierarchy, IconUsers, IconChartBar, IconSettings, IconSun, IconMoon,
  IconLanguage, IconLogout, IconServer, IconChevronRight, IconCirclePlus,
  IconRefresh, IconPlayerPlay, IconFileText, IconQrcode, IconLink, IconX, IconSearch,
} from "@tabler/icons-react";

const icons = {
  home: IconHome, tunnels: IconHierarchy, clients: IconUsers, traffic: IconChartBar,
  settings: IconSettings, sun: IconSun, moon: IconMoon, language: IconLanguage,
  logout: IconLogout, server: IconServer, chevron: IconChevronRight, plus: IconCirclePlus,
  refresh: IconRefresh, play: IconPlayerPlay, file: IconFileText, qr: IconQrcode,
  link: IconLink, close: IconX, search: IconSearch,
};
export type IconName = keyof typeof icons;
export function Icon({ name }: { name: IconName }) {
  const Component = icons[name];
  return <Component className="ui-icon" size={20} stroke={1.7} aria-hidden="true" />;
}
export function ForgeMark() {
  return <svg width="38" height="38" viewBox="0 0 40 40" aria-hidden="true" fill="currentColor"><path d="M20 1 0 36h9L24 9Z" /><path opacity=".8" d="m26 12-9 5 10 19h13Z" /><path opacity=".65" d="m16 23-7 13h14Z" /></svg>;
}
