import { useEffect, useState } from 'react';
import * as api from '@/api/client';
import type { TLSReport } from '@/api/client';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { ScrollArea } from '@/components/ui/scroll-area';
import { ShieldCheck, ChevronLeft, ChevronRight } from 'lucide-react';

export function TLSReportsView() {
  const [loading, setLoading] = useState(true);
  const [reports, setReports] = useState<TLSReport[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const limit = 20;

  const loadReports = async (off: number) => {
    setLoading(true);
    try {
      const res = await api.listTLSReports({ limit, offset: off });
      setReports(res.data || []);
      setTotal(res.pagination?.total ?? 0);
      setOffset(off);
    } catch {
      setReports([]);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadReports(0);
  }, []);

  const policyBadgeVariant = (type: string) => {
    if (type === 'sts') return 'default' as const;
    if (type === 'tlsa') return 'secondary' as const;
    return 'outline' as const;
  };

  const successRate = (s: number, f: number) => {
    const total = s + f;
    if (total === 0) return '—';
    return `${((s / total) * 100).toFixed(1)}%`;
  };

  if (loading && reports.length === 0) {
    return (
      <div className="p-6 flex items-center justify-center">
        <div className="animate-pulse text-muted-foreground">Loading TLS reports...</div>
      </div>
    );
  }

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ShieldCheck className="w-5 h-5" />
            TLS-RPT Reports
            {total > 0 && (
              <Badge variant="secondary" className="ml-2">{total}</Badge>
            )}
          </CardTitle>
          <CardDescription>
            TLS connectivity reports received from external mail servers (RFC 8460).
          </CardDescription>
        </CardHeader>
        <CardContent>
          {reports.length === 0 ? (
            <div className="text-center py-8 text-muted-foreground">
              No TLS reports received yet.
            </div>
          ) : (
            <>
              <ScrollArea className="max-h-[60vh]">
                <div className="space-y-2">
                  {reports.map(report => (
                    <div
                      key={report.id}
                      className="flex items-start gap-3 p-3 rounded-lg border bg-card hover:bg-accent/50 transition-colors"
                    >
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2 mb-1">
                          <span className="font-medium text-sm">{report.reporting_org}</span>
                          <Badge variant={policyBadgeVariant(report.policy_type)} className="text-xs">
                            {report.policy_type}
                          </Badge>
                        </div>
                        <div className="text-sm text-muted-foreground">{report.policy_domain}</div>
                        <div className="flex items-center gap-4 mt-1 text-xs text-muted-foreground">
                          <span>
                            {new Date(report.start_date).toLocaleDateString()} – {new Date(report.end_date).toLocaleDateString()}
                          </span>
                          <span className="text-green-600 dark:text-green-400">
                            {report.total_successful} successful
                          </span>
                          {report.total_failure > 0 && (
                            <span className="text-red-600 dark:text-red-400">
                              {report.total_failure} failed
                            </span>
                          )}
                          <span>
                            Success: {successRate(report.total_successful, report.total_failure)}
                          </span>
                        </div>
                      </div>
                      <div className="text-xs text-muted-foreground shrink-0">
                        {new Date(report.received_at).toLocaleString()}
                      </div>
                    </div>
                  ))}
                </div>
              </ScrollArea>

              {/* Pagination */}
              {total > limit && (
                <div className="flex items-center justify-between mt-4 pt-4 border-t">
                  <span className="text-sm text-muted-foreground">
                    Showing {offset + 1}–{Math.min(offset + limit, total)} of {total}
                  </span>
                  <div className="flex gap-1">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={offset === 0 || loading}
                      onClick={() => loadReports(Math.max(0, offset - limit))}
                    >
                      <ChevronLeft className="w-4 h-4" />
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={offset + limit >= total || loading}
                      onClick={() => loadReports(offset + limit)}
                    >
                      <ChevronRight className="w-4 h-4" />
                    </Button>
                  </div>
                </div>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
