import { sortBy } from '@shell/utils/sort';

const ENDPOINT = 'api.hetzner.cloud/v1';

function addParam(url, key, val) {
  let out = url + (url.includes('?') ? '&' : '?');

  if (!Array.isArray(val)) {
    val = [val];
  }

  out += val.map((v) => {
    if (v === null) {
      return `${ encodeURIComponent(key) }`;
    } else {
      return `${ encodeURIComponent(key) }=${ encodeURIComponent(v) }`;
    }
  }).join('&');

  return out;
}

const VALID_IMAGES = [
  /^ubuntu-\d+\.\d+$/,
  /^debian-\d+$/,
  /^centos(-stream)?-\d+$/,
  /^fedora-\d+$/,
  /^rocky-\d+$/,
  /^alma-\d+$/,
];

export const state = () => {
  return { cache: {} };
};

export const mutations = {
  setCache(state, { credentialId, key, value }) {
    let cache = state.cache[credentialId];

    if (!cache) {
      cache = {};
      state.cache[credentialId] = cache;
    }

    cache[key] = value;
  },
};

export const getters = {
  fromCache: (state) => ({ credentialId, key }) => {
    return state.cache[credentialId]?.[key];
  },
};

export const actions = {
  async locationOptions({ dispatch }, { credentialId }) {
    const data = await dispatch('cachedCommand', { credentialId, command: 'locations' });

    const out = (data.locations || []).map((loc) => {
      return {
        label: `${ loc.description } (${ loc.name })`,
        value: loc.name,
      };
    });

    return sortBy(out, 'label');
  },

  async serverTypeOptions({ dispatch }, { credentialId }) {
    const data = await dispatch('cachedCommand', { credentialId, command: 'server_types' });

    const out = (data.server_types || [])
      .filter((st) => !st.deprecation || !st.deprecation.announced)
      .map((st) => {
        const memoryGb = st.memory;
        const disk = st.disk;
        const vcpus = st.cores;

        return {
          label:        `${ st.name.toUpperCase() } - ${ vcpus } vCPUs, ${ memoryGb } GB RAM, ${ disk } GB Disk`,
          value:        st.name,
          architecture: st.architecture,
          vcpus,
          memoryGb,
          disk,
        };
      });

    return sortBy(out, ['vcpus', 'memoryGb', 'disk']);
  },

  async imageOptions({ dispatch }, { credentialId, architecture = 'x86' }) {
    const data = await dispatch('cachedCommand', { credentialId, command: 'images', params: { type: 'system', status: 'available', architecture } });

    const out = (data.images || [])
      .filter((img) => {
        if (!img.name) {
          return false;
        }

        for (const re of VALID_IMAGES) {
          if (img.name.match(re)) {
            return true;
          }
        }

        return false;
      })
      .map((img) => {
        const label = img.description || `${ img.os_flavor } ${ img.os_version }`;

        return {
          label,
          value: img.name,
        };
      });

    return sortBy(out, 'label');
  },

  async networkOptions({ dispatch }, { credentialId }) {
    const data = await dispatch('cachedCommand', { credentialId, command: 'networks' });

    return (data.networks || []).map((net) => {
      return {
        label: `${ net.name } (${ net.ip_range })`,
        value: `${ net.id }`,
      };
    });
  },

  async firewallOptions({ dispatch }, { credentialId }) {
    const data = await dispatch('cachedCommand', { credentialId, command: 'firewalls' });

    return (data.firewalls || []).map((fw) => {
      return {
        label: fw.name,
        value: `${ fw.id }`,
      };
    });
  },

  async sshKeyOptions({ dispatch }, { credentialId }) {
    const data = await dispatch('cachedCommand', { credentialId, command: 'ssh_keys' });

    return (data.ssh_keys || []).map((key) => {
      return {
        label: `${ key.name } (${ key.fingerprint })`,
        value: `${ key.id }`,
      };
    });
  },

  async cachedCommand({ getters, commit, dispatch }, { credentialId, command, params }) {
    const cacheKey = params ? `${ command }:${ JSON.stringify(params) }` : command;
    let out = getters['fromCache']({ credentialId, key: cacheKey });

    if (!out) {
      out = await dispatch('request', { credentialId, command, params });
      commit('setCache', { credentialId, key: cacheKey, value: out });
    }

    return out;
  },

  async request({ dispatch }, { token, credentialId, command, params }) {
    let url = `/meta/proxy/${ ENDPOINT }/${ command }`;

    url = addParam(url, 'per_page', 500);

    if (params) {
      for (const [key, val] of Object.entries(params)) {
        url = addParam(url, key, val);
      }
    }

    const headers = { Accept: 'application/json' };

    if (credentialId) {
      headers['x-api-cattleauth-header'] = `Bearer credID=${ credentialId } passwordField=apiToken`;
    } else if (token) {
      headers['x-api-auth-header'] = `Bearer ${ token }`;
    }

    const res = await dispatch('management/request', {
      url,
      headers,
      redirectUnauthorized: false,
    }, { root: true });

    return res;
  },
};
