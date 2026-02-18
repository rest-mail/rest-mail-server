import { useState, useEffect } from 'react';
import { toast } from 'sonner';
import * as api from '@/api/client';
import type { PipelineData, PipelineTestResult } from '@/api/client';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import { ScrollArea } from '@/components/ui/scroll-area';
import { FlaskConical, Play, CheckCircle2, XCircle, MinusCircle, Clock } from 'lucide-react';

const SAMPLE_EMAIL = {
  from: 'sender@example.com',
  to: ['recipient@mail1.test'],
  subject: 'Test email for pipeline',
  body: {
    content_type: 'text/plain',
    content: 'This is a test email to verify pipeline filter behavior.',
  },
  headers: {
    'Message-ID': '<test-001@example.com>',
    'Date': new Date().toUTCString(),
  },
};

export function PipelineTesterView() {
  const [pipelines, setPipelines] = useState<PipelineData[]>([]);
  const [selectedPipeline, setSelectedPipeline] = useState<number | null>(null);
  const [emailJson, setEmailJson] = useState(JSON.stringify(SAMPLE_EMAIL, null, 2));
  const [result, setResult] = useState<PipelineTestResult | null>(null);
  const [running, setRunning] = useState(false);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api.listPipelines()
      .then(res => {
        const items = res.data || [];
        setPipelines(items);
        if (items.length > 0) setSelectedPipeline(items[0].id);
      })
      .catch(() => setPipelines([]))
      .finally(() => setLoading(false));
  }, []);

  const handleTest = async () => {
    if (!selectedPipeline) return;
    let email: Record<string, unknown>;
    try {
      email = JSON.parse(emailJson);
    } catch {
      toast.error('Invalid JSON in email field');
      return;
    }
    setRunning(true);
    setResult(null);
    try {
      const res = await api.testPipeline(selectedPipeline, email);
      setResult(res.data);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Pipeline test failed');
    } finally {
      setRunning(false);
    }
  };

  const actionIcon = (action: string) => {
    switch (action) {
      case 'continue': return <CheckCircle2 className="w-4 h-4 text-green-500" />;
      case 'reject': case 'drop': return <XCircle className="w-4 h-4 text-red-500" />;
      case 'quarantine': return <MinusCircle className="w-4 h-4 text-yellow-500" />;
      default: return <CheckCircle2 className="w-4 h-4 text-muted-foreground" />;
    }
  };

  const actionBadge = (action: string) => {
    const variant = action === 'continue' ? 'default' as const
      : action === 'reject' || action === 'drop' ? 'destructive' as const
      : 'secondary' as const;
    return <Badge variant={variant} className="text-xs">{action}</Badge>;
  };

  if (loading) {
    return (
      <div className="p-6 flex items-center justify-center">
        <div className="animate-pulse text-muted-foreground">Loading pipelines...</div>
      </div>
    );
  }

  return (
    <div className="p-6 max-w-5xl mx-auto space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <FlaskConical className="w-5 h-5" />
            Pipeline Tester
          </CardTitle>
          <CardDescription>
            Send a sample email through a pipeline and see the result of each filter.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* Pipeline selector */}
          <div>
            <Label htmlFor="pipeline-select">Pipeline</Label>
            <select
              id="pipeline-select"
              value={selectedPipeline ?? ''}
              onChange={e => setSelectedPipeline(Number(e.target.value))}
              className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {pipelines.map(p => (
                <option key={p.id} value={p.id}>
                  #{p.id} — {p.direction} (Domain {p.domain_id}) {p.active ? '' : '[inactive]'}
                </option>
              ))}
            </select>
          </div>

          {/* Email JSON */}
          <div>
            <Label htmlFor="email-json">Sample Email (JSON)</Label>
            <textarea
              id="email-json"
              rows={10}
              value={emailJson}
              onChange={e => setEmailJson(e.target.value)}
              className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm font-mono ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>

          <Button onClick={handleTest} disabled={running || !selectedPipeline}>
            <Play className="w-4 h-4 mr-1" />
            {running ? 'Running...' : 'Run Test'}
          </Button>
        </CardContent>
      </Card>

      {/* Results */}
      {result && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              Result: {actionBadge(result.action)}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ScrollArea className="max-h-[40vh]">
              <div className="space-y-1">
                {result.logs?.map((log, i) => (
                  <div
                    key={i}
                    className="flex items-center gap-3 p-2 rounded border bg-card text-sm"
                  >
                    {actionIcon(log.action)}
                    <span className="font-mono font-medium w-36 shrink-0">{log.filter}</span>
                    {actionBadge(log.action)}
                    <span className="flex-1 text-muted-foreground truncate">{log.message}</span>
                    <span className="text-xs text-muted-foreground flex items-center gap-1 shrink-0">
                      <Clock className="w-3 h-3" />
                      {log.duration_ms}ms
                    </span>
                  </div>
                ))}
              </div>
            </ScrollArea>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
