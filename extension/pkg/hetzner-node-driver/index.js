import { importTypes } from '@rancher/auto-import';
import * as hetznerStore from './store/hetzner';

export default function(plugin) {
  importTypes(plugin);
  plugin.metadata = require('./package.json');

  plugin.addStore(
    'hetzner',
    () => (store) => {
      store.registerModule('hetzner', {
        namespaced: true,
        state:      hetznerStore.state,
        getters:    hetznerStore.getters,
        mutations:  hetznerStore.mutations,
        actions:    hetznerStore.actions,
      });
    },
    (store) => {
      store.unregisterModule('hetzner');
    }
  );
}
