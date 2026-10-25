import React from 'react';
import {
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Copy,
  ExternalLink,
  Globe,
  Info,
  KeyRound,
  Lock,
  LogOut,
  Mail,
  LucideIcon,
  Plus,
  RefreshCw,
  Server,
  Settings,
  Shield,
  Trash2,
  TriangleAlert,
  X,
} from 'lucide-react-native';

export type IconName =
  | 'gear'
  | 'chevron-left'
  | 'chevron-right'
  | 'chevron-down'
  | 'trash'
  | 'close'
  | 'check'
  | 'copy'
  | 'shield'
  | 'lock'
  | 'plus'
  | 'info'
  | 'warning'
  | 'retry'
  | 'server'
  | 'globe'
  | 'external'
  | 'key'
  | 'logout'
  | 'mail';

const ICONS: Record<IconName, LucideIcon> = {
  gear: Settings,
  'chevron-left': ChevronLeft,
  'chevron-right': ChevronRight,
  'chevron-down': ChevronDown,
  trash: Trash2,
  close: X,
  check: Check,
  copy: Copy,
  shield: Shield,
  lock: Lock,
  plus: Plus,
  info: Info,
  warning: TriangleAlert,
  retry: RefreshCw,
  server: Server,
  globe: Globe,
  external: ExternalLink,
  key: KeyRound,
  logout: LogOut,
  mail: Mail,
};

export function Icon({
  name,
  size = 20,
  color = '#000',
  strokeWidth = 2,
}: {
  name: IconName;
  size?: number;
  color?: string;
  strokeWidth?: number;
}): React.JSX.Element {
  const IconComponent = ICONS[name];
  return <IconComponent size={size} color={color} strokeWidth={strokeWidth} fill="none" />;
}
