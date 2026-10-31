<script lang="ts">
  import type { DiffFile } from '../../diff';
  import type { Finding } from '../../types';
  import Icon from '../../Icon.svelte';
  import { mdiChevronDown, mdiChevronRight } from '../../icons';
  import FindingCard from './FindingCard.svelte';
  import Tokens from '../../components/Tokens.svelte';
  import { highlightDiff, languageOf } from '../../highlight';
  import { Highlighted } from '../../tokens.svelte';

  let {
    file,
    findings,
    slug,
    pullUrl,
    open = $bindable(false),
    id,
  }: { file: DiffFile; findings: Finding[]; slug: string; pullUrl: string; open?: boolean; id: string } = $props();
  const contentId = $props.id();

  // Findings keyed by the new-side line they point at. Anything that points
  // outside the hunks shown is listed under the file header instead.
  const byLine = $derived.by(() => {
    const m = new Map<number, Finding[]>();
    for (const f of findings) m.set(f.line, [...(m.get(f.line) ?? []), f]);
    return m;
  });
  const shownLines = $derived(new Set(file.lines.map((l) => l.newNo).filter((n) => n !== null)));
  const orphans = $derived(findings.filter((f) => !shownLines.has(f.line)));

  // The lines' tokens, once the file is open and its language loads.
  // Plain text until then, and for a language that is not highlighted.
  const tokens = new Highlighted(
    () => file,
    (f) => highlightDiff(f.lines, languageOf(f.path)),
    () => open,
  );
  const highlighted = $derived(tokens.value);
</script>

<section class="diff-file bg-panel border border-solid border-line rounded-lg min-w-0" {id}>
  <h3 class="diff-file-head m-0 sticky top-0 z-[var(--z-sticky)] bg-panel border-b border-solid border-b-line rounded-[var(--radius-lg)_var(--radius-lg)_0_0]">
    <button aria-expanded={open} aria-controls={open ? contentId : undefined} onclick={() => (open = !open)}>
      <Icon path={open ? mdiChevronDown : mdiChevronRight} size={14} />
      <span class="mono diff-path font-semibold wrap-anywhere">{file.oldPath && file.oldPath !== file.newPath && file.newPath ? `${file.oldPath} → ${file.newPath}` : file.path}</span>
      <span class="small text-ui-sm"><span class="add text-success">+{file.added}</span> <span class="del text-danger">−{file.removed}</span></span>
      {#if !open}<span class="small muted text-ui-sm">{file.lines.length} lines, collapsed</span>{/if}
      {#if findings.length}<span class="badge inline-flex items-center gap-1 text-ui-xs leading-[1.5] border border-solid border-line py-0 px-[7px] text-muted whitespace-nowrap rounded-[3px] font-medium">{findings.length} finding{findings.length === 1 ? '' : 's'}</span>{/if}
    </button>
  </h3>
  {#if open}
    <div id={contentId}>
      {#each orphans as f (f.id)}<div class="diff-finding"><FindingCard {f} {slug} {pullUrl} id={`diff-finding-${f.id}`} /></div>{/each}
      {#if file.lines.length === 0}
        <p class="small muted diff-empty text-ui-sm m-0 py-2 px-3">{file.header.slice(1).join(' · ') || 'No textual changes.'}</p>
      {:else}
        <div class="table-wrap overflow-x-auto max-w-full">
          <table class="diff">
            <tbody>
              {#each file.lines as l, i (i)}
                {@const anchored = l.newNo !== null && l.kind !== 'del' ? byLine.get(l.newNo) : undefined}
                <tr class="dl dl-{l.kind}" class:dl-marked={anchored}>
                  <td class="ln" aria-hidden="true">{l.oldNo ?? ''}</td>
                  <td class="ln" aria-hidden="true">{l.newNo ?? ''}</td>
                  <td class="code"><span class="sign inline-block w-[1.2em] text-muted select-none" aria-hidden="true">{l.kind === 'add' ? '+' : l.kind === 'del' ? '-' : ' '}</span>{#if highlighted?.[i]}<Tokens line={highlighted[i]} />{:else}{l.text}{/if}</td>
                </tr>
                {#if anchored}
                  <tr class="dl-finding">
                    <td colspan="3">
                      {#each anchored as f (f.id)}<div class="diff-finding"><FindingCard {f} {slug} {pullUrl} compact id={`diff-finding-${f.id}`} /></div>{/each}
                    </td>
                  </tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  {/if}
</section>
