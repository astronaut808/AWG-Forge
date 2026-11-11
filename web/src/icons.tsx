import {
  IconHome, IconHierarchy, IconUsers, IconChartBar, IconSettings, IconSun, IconMoon,
  IconLanguage, IconLogout, IconServer, IconChevronRight, IconCirclePlus,
  IconRefresh, IconPlayerPlay, IconFileText, IconQrcode, IconLink, IconX, IconSearch, IconPencil,
} from "@tabler/icons-react";

const icons = {
  home: IconHome, tunnels: IconHierarchy, clients: IconUsers, traffic: IconChartBar,
  settings: IconSettings, sun: IconSun, moon: IconMoon, language: IconLanguage,
  logout: IconLogout, server: IconServer, chevron: IconChevronRight, plus: IconCirclePlus,
  refresh: IconRefresh, play: IconPlayerPlay, file: IconFileText, qr: IconQrcode,
  link: IconLink, close: IconX, search: IconSearch, edit: IconPencil,
};
export type IconName = keyof typeof icons;
export function Icon({ name }: { name: IconName }) {
  const Component = icons[name];
  return <Component className="ui-icon" size={20} stroke={1.7} aria-hidden="true" />;
}
export function ForgeMark() {
  return <img src={`${import.meta.env.BASE_URL}forge-mark.svg`} width="42" height="42" alt="" />;
}
