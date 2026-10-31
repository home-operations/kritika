<script lang="ts">
  import type { KeyboardEventHandler } from 'svelte/elements';
  // A search box that also takes filter tokens and suggests them: a
  // combobox whose list opens while the box has focus and something
  // matches. It reports the parsed text once typing pauses, so typing
  // does not fire a request per keystroke.
  import { tick } from 'svelte';
  import { Combobox } from 'bits-ui';
  import { parseTokens, suggest, complete, type Parsed, type TokenSpec } from '../tokensearch';

  let {
    id,
    label,
    placeholder,
    specs,
    text = $bindable(''),
    input = $bindable(),
    ready = true,
    onapply,
  }: {
    id: string;
    label: string;
    placeholder: string;
    specs: readonly TokenSpec[];
    text?: string;
    input?: HTMLInputElement;
    // Closed token values can depend on data that has not loaded yet.
    ready?: boolean;
    onapply: (p: Parsed) => void;
  } = $props();

  let pending: ReturnType<typeof setTimeout> | undefined;
  function apply(): void {
    clearTimeout(pending);
    pending = setTimeout(() => onapply(parseTokens(text, specs)), 250);
  }
  $effect(() => () => clearTimeout(pending));

  const unknown = $derived(ready ? parseTokens(text, specs).unknown : []);

  let open = $state(false);
  let selected = $state('');
  let keyboardSelection = $state(false);
  const suggestions = $derived(suggest(text, specs));
  const optionId = (label: string) => `${id}-suggest-${encodeURIComponent(label)}`;

  async function accept(value: string): Promise<void> {
    text = value;
    apply();
    // Completing a token key opens its values. Completing a value offers
    // the next token while keeping the caret in the same input.
    await tick();
    selected = '';
    keyboardSelection = false;
    open = suggestions.length > 0;
    input?.focus();
  }
</script>

<Combobox.Root
  type="single"
  bind:value={selected}
  inputValue={text}
  bind:open={() => open && suggestions.length > 0, (value) => (open = value)}
  onValueChange={accept}
  allowDeselect={false}
  loop
>
  <div class="search-box search-combo relative [flex:1_1_240px] min-w-0 flex">
    <label class="sr-only" for={id}>{label}</label>
    <Combobox.Input
      {id}
      type="search"
      autocomplete="off"
      aria-controls={open && suggestions.length ? `${id}-suggest` : undefined}
      {placeholder}
      oninput={(e) => {
        text = e.currentTarget.value;
        keyboardSelection = false;
        open = true;
        apply();
      }}
      onfocus={() => { open = true; keyboardSelection = false; }}
      onblur={() => (open = false)}
      onkeydown={(e) => {
        if (['ArrowDown', 'ArrowUp'].includes(e.key)) keyboardSelection = true;
        if (e.key !== 'Tab' || e.shiftKey || !keyboardSelection || !open || e.isComposing) return;
        const active = suggestions.find((s) => e.currentTarget.getAttribute('aria-activedescendant') === optionId(s.label));
        if (active) {
          e.preventDefault();
          void accept(complete(text, active));
        }
      }}
    >
      {#snippet child({ props })}
        {@const active = suggestions.find((s) => optionId(s.label) === props['aria-activedescendant'])}
        <!-- A live refresh can remove the highlighted option without closing the list. -->
        <input
          {...props}
          aria-activedescendant={active ? optionId(active.label) : undefined}
          onkeydown={(e) => {
            if (['Home', 'End', 'PageUp', 'PageDown'].includes(e.key)) return;
            if (e.key === 'Enter' && (!keyboardSelection || e.isComposing)) return;
            (props.onkeydown as KeyboardEventHandler<HTMLInputElement> | undefined)?.(e);
          }}
          bind:this={input}
        />
      {/snippet}
    </Combobox.Input>
    <Combobox.ContentStatic class="suggest absolute top-[calc(100%_+_4px)] left-0 right-0 z-[var(--z-popup)] m-0 py-1 px-0 list-none bg-panel border border-solid border-line rounded-lg shadow-[0_8px_24px_color-mix(in_srgb,_var(--text)_12%,_transparent)]" id="{id}-suggest" aria-label="Suggestions" preventScroll={false}>
      {#each suggestions as s (s.label)}
        <Combobox.Item
          id={optionId(s.label)}
          value={complete(text, s)}
          label={complete(text, s)}
        >
          <span class="mono">{s.label}</span>
          {#if s.hint}<span class="muted small text-ui-sm">{s.hint}</span>{/if}
        </Combobox.Item>
      {/each}
    </Combobox.ContentStatic>
  </div>
</Combobox.Root>

{#if unknown.length}
  <p class="small muted token-note text-ui-sm [flex-basis:100%] [order:1] m-0" role="note">Not filtering by {unknown.join(', ')}: nothing by that name.</p>
{/if}
