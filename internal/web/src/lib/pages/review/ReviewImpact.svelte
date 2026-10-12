<script lang="ts">
  import Markdown from '../../components/Markdown.svelte';
  let { notes }: { notes: string[] } = $props();
</script>

<Markdown text={notes.map((note) => `- ${note.replaceAll('\n', '\n  ')}`).join('\n')} />
