<script>
  import { tagLabel } from '../lib/status.js';

  export let status;
</script>

<span class={'tag ' + status}>{tagLabel(status)}</span>
