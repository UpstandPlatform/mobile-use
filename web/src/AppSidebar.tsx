import { Activity, Bot, Download, LayoutDashboard, LogOut, ScrollText, Smartphone } from 'lucide-react';
import type { Actor } from './api';
import { Badge } from '@/components/ui/badge';
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarSeparator,
} from '@/components/ui/sidebar';

export type ConsolePage = 'Overview' | 'Devices' | 'Sessions' | 'Agent setup' | 'Downloads' | 'Audit';

const workspaceItems = [
  { label: 'Overview', icon: LayoutDashboard },
  { label: 'Devices', icon: Smartphone },
  { label: 'Sessions', icon: Activity },
] as const;

const setupItems = [
  { label: 'Agent setup', icon: Bot },
  { label: 'Downloads', icon: Download },
] as const;

export function AppSidebar({
  page,
  setPage,
  actor,
  version,
  sessionsCount,
  onSignOut,
}: {
  page: ConsolePage;
  setPage: (page: ConsolePage) => void;
  actor: Actor;
  version?: string;
  sessionsCount: number;
  onSignOut: () => void;
}) {
  const showAudit = ['admin', 'auditor'].includes(actor.role);

  return (
    <Sidebar collapsible="icon" variant="inset">
      <SidebarHeader className="gap-3 p-4">
        <div className="flex min-w-0 items-center gap-3">
          <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-primary text-primary-foreground">
            <Smartphone />
          </span>
          <div className="grid min-w-0 gap-0.5 group-data-[collapsible=icon]:hidden">
            <span className="truncate font-semibold tracking-tight">mobile-use</span>
            <span className="truncate text-xs text-muted-foreground">Device workspace</span>
          </div>
        </div>
        <Badge className="w-fit group-data-[collapsible=icon]:hidden" variant="secondary">
          {actor.workspace === 'local' ? 'Local workstation' : actor.workspace}
        </Badge>
      </SidebarHeader>

      <SidebarSeparator />

      <SidebarContent className="px-2 py-3">
        <SidebarGroup>
          <SidebarGroupLabel>Workspace</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {workspaceItems.map((item) => (
                <SidebarMenuItem key={item.label}>
                  <SidebarMenuButton
                    isActive={page === item.label}
                    tooltip={item.label}
                    onClick={() => setPage(item.label)}
                  >
                    <item.icon />
                    <span>{item.label}</span>
                    {item.label === 'Sessions' && sessionsCount > 0 && (
                      <SidebarMenuBadge>{sessionsCount}</SidebarMenuBadge>
                    )}
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel>Set up</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {setupItems.map((item) => (
                <SidebarMenuItem key={item.label}>
                  <SidebarMenuButton
                    isActive={page === item.label}
                    tooltip={item.label}
                    onClick={() => setPage(item.label)}
                  >
                    <item.icon />
                    <span>{item.label}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        {showAudit && (
          <SidebarGroup>
            <SidebarGroupLabel>Administration</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    isActive={page === 'Audit'}
                    tooltip="Audit"
                    onClick={() => setPage('Audit')}
                  >
                    <ScrollText />
                    <span>Audit log</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
      </SidebarContent>

      <SidebarFooter className="gap-2 p-3">
        {version && (
          <div className="px-3 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
            mobile-use <span className="font-medium text-foreground">{version}</span>
          </div>
        )}
        <div className="grid gap-1 rounded-lg bg-muted p-3 group-data-[collapsible=icon]:hidden">
          <span className="truncate text-sm font-medium">{actor.subject}</span>
          <span className="truncate text-xs text-muted-foreground">{actor.role}</span>
        </div>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton tooltip="Sign out" onClick={onSignOut}>
              <LogOut />
              <span>Sign out</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
    </Sidebar>
  );
}
