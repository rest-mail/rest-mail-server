import { useEffect, useState, useCallback } from 'react';
import { toast } from 'sonner';
import * as api from '@/api/client';
import type { PipelineData, FilterConfig } from '@/api/client';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Separator } from '@/components/ui/separator';
import { Settings2, GripVertical, Power, PowerOff, ChevronUp, ChevronDown, Trash2, Save } from 'lucide-react';

export function PipelineConfigView() {
  const [pipelines, setPipelines] = useState<PipelineData[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [filters, setFilters] = useState<FilterConfig[]>([]);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [loading, setLoading] = useState(true);

  const loadPipelines = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.listPipelines();
      const items = res.data || [];
      setPipelines(items);
      if (items.length > 0 && !selectedId) {
        setSelectedId(items[0].id);
        setFilters(items[0].filters || []);
      }
    } catch {
      setPipelines([]);
    } finally {
      setLoading(false);
    }
  }, [selectedId]);

  useEffect(() => {
    loadPipelines();
  }, [loadPipelines]);

  const selectPipeline = (p: PipelineData) => {
    if (dirty) {
      if (!confirm('You have unsaved changes. Switch pipeline anyway?')) return;
    }
    setSelectedId(p.id);
    setFilters(p.filters || []);
    setDirty(false);
  };

  const moveFilter = (index: number, direction: -1 | 1) => {
    const target = index + direction;
    if (target < 0 || target >= filters.length) return;
    const updated = [...filters];
    [updated[index], updated[target]] = [updated[target], updated[index]];
    setFilters(updated);
    setDirty(true);
  };

  const toggleFilter = (index: number) => {
    const updated = [...filters];
    updated[index] = { ...updated[index], enabled: !updated[index].enabled };
    setFilters(updated);
    setDirty(true);
  };

  const removeFilter = (index: number) => {
    setFilters(prev => prev.filter((_, i) => i !== index));
    setDirty(true);
  };

  const handleSave = async () => {
    if (!selectedId) return;
    setSaving(true);
    try {
      await api.updatePipeline(selectedId, { filters });
      setDirty(false);
      toast.success('Pipeline saved');
      await loadPipelines();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to save pipeline');
    } finally {
      setSaving(false);
    }
  };

  const togglePipelineActive = async (p: PipelineData) => {
    try {
      await api.updatePipeline(p.id, { active: !p.active });
      toast.success(p.active ? 'Pipeline deactivated' : 'Pipeline activated');
      await loadPipelines();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to toggle pipeline');
    }
  };

  const selected = pipelines.find(p => p.id === selectedId);

  if (loading) {
    return (
      <div className="p-6 flex items-center justify-center">
        <div className="animate-pulse text-muted-foreground">Loading pipelines...</div>
      </div>
    );
  }

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <div className="grid grid-cols-[280px_1fr] gap-4 h-[calc(100vh-8rem)]">
        {/* Pipeline list */}
        <Card className="flex flex-col">
          <CardHeader className="pb-2">
            <CardTitle className="flex items-center gap-2 text-base">
              <Settings2 className="w-4 h-4" />
              Pipelines
            </CardTitle>
          </CardHeader>
          <CardContent className="flex-1 p-0">
            <ScrollArea className="h-full">
              <div className="px-2 py-1 space-y-0.5">
                {pipelines.map(p => (
                  <button
                    key={p.id}
                    onClick={() => selectPipeline(p)}
                    className={`w-full text-left px-3 py-2 rounded-md text-sm transition-colors ${
                      selectedId === p.id
                        ? 'bg-accent text-accent-foreground'
                        : 'hover:bg-accent/50'
                    }`}
                  >
                    <div className="flex items-center gap-2">
                      <Badge variant={p.direction === 'inbound' ? 'default' : 'secondary'} className="text-xs">
                        {p.direction}
                      </Badge>
                      <span className="truncate">Domain {p.domain_id}</span>
                      {!p.active && (
                        <Badge variant="outline" className="text-xs ml-auto">off</Badge>
                      )}
                    </div>
                    <div className="text-xs text-muted-foreground mt-0.5">
                      {(p.filters || []).length} filters
                    </div>
                  </button>
                ))}
              </div>
            </ScrollArea>
          </CardContent>
        </Card>

        {/* Filter editor */}
        <Card className="flex flex-col">
          <CardHeader className="pb-2">
            <div className="flex items-center justify-between">
              <div>
                <CardTitle className="text-base">
                  {selected ? `${selected.direction} pipeline` : 'Select a pipeline'}
                </CardTitle>
                {selected && (
                  <CardDescription>
                    Domain {selected.domain_id} — {(selected.filters || []).length} filters
                  </CardDescription>
                )}
              </div>
              {selected && (
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => togglePipelineActive(selected)}
                  >
                    {selected.active ? <PowerOff className="w-4 h-4 mr-1" /> : <Power className="w-4 h-4 mr-1" />}
                    {selected.active ? 'Deactivate' : 'Activate'}
                  </Button>
                  <Button size="sm" disabled={!dirty || saving} onClick={handleSave}>
                    <Save className="w-4 h-4 mr-1" />
                    {saving ? 'Saving...' : 'Save'}
                  </Button>
                </div>
              )}
            </div>
          </CardHeader>
          <Separator />
          <CardContent className="flex-1 p-0 overflow-hidden">
            {!selected ? (
              <div className="flex items-center justify-center h-full text-muted-foreground">
                Select a pipeline from the list
              </div>
            ) : filters.length === 0 ? (
              <div className="flex items-center justify-center h-full text-muted-foreground">
                No filters configured
              </div>
            ) : (
              <ScrollArea className="h-full">
                <div className="p-3 space-y-1">
                  {filters.map((filter, index) => (
                    <div
                      key={`${filter.name}-${index}`}
                      className={`flex items-center gap-2 p-2 rounded-lg border transition-colors ${
                        filter.enabled ? 'bg-card' : 'bg-muted/50 opacity-60'
                      }`}
                    >
                      <GripVertical className="w-4 h-4 text-muted-foreground shrink-0 cursor-grab" />

                      <span className="text-sm font-medium font-mono flex-1 truncate">
                        {filter.name}
                      </span>

                      <Badge variant={filter.enabled ? 'default' : 'outline'} className="text-xs">
                        {filter.enabled ? 'on' : 'off'}
                      </Badge>

                      {/* Reorder buttons */}
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 w-7 p-0"
                        disabled={index === 0}
                        onClick={() => moveFilter(index, -1)}
                        title="Move up"
                      >
                        <ChevronUp className="w-3.5 h-3.5" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 w-7 p-0"
                        disabled={index === filters.length - 1}
                        onClick={() => moveFilter(index, 1)}
                        title="Move down"
                      >
                        <ChevronDown className="w-3.5 h-3.5" />
                      </Button>

                      {/* Toggle */}
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 w-7 p-0"
                        onClick={() => toggleFilter(index)}
                        title={filter.enabled ? 'Disable' : 'Enable'}
                      >
                        {filter.enabled
                          ? <Power className="w-3.5 h-3.5 text-green-500" />
                          : <PowerOff className="w-3.5 h-3.5 text-muted-foreground" />}
                      </Button>

                      {/* Remove */}
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 w-7 p-0 text-destructive hover:text-destructive"
                        onClick={() => removeFilter(index)}
                        title="Remove filter"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </Button>
                    </div>
                  ))}
                </div>
              </ScrollArea>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
