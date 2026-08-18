package com.agentquay;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** JsonSchemaGenerator 单测：类型 → JSON Schema。 */
class SchemaGenTest {

    private final JsonSchemaGenerator gen = new JsonSchemaGenerator();

    @SuppressWarnings("unused")
    static class Controller {
        public void primitives(String keyword, int limit, long count, double ratio,
                               boolean enabled) {
        }

        public void optional(String keyword, Optional<String> tag) {
        }

        public void containers(List<String> items, Map<String, Integer> counts) {
        }

        public void pojo(Song song) {
        }
    }

    @SuppressWarnings("unused")
    static class Song {
        public String id;
        public String title;
        public int duration;
    }

    @Test
    void primitives() throws Exception {
        Map<String, Object> schema = gen.paramSchema(
                Controller.class.getMethod("primitives", String.class, int.class, long.class,
                        double.class, boolean.class));
        @SuppressWarnings("unchecked")
        Map<String, Object> props = (Map<String, Object>) schema.get("properties");
        assertEquals("string", ((Map<?, ?>) props.get("keyword")).get("type"));
        assertEquals("integer", ((Map<?, ?>) props.get("limit")).get("type"));
        assertEquals("integer", ((Map<?, ?>) props.get("count")).get("type"));
        assertEquals("number", ((Map<?, ?>) props.get("ratio")).get("type"));
        assertEquals("boolean", ((Map<?, ?>) props.get("enabled")).get("type"));
        assertEquals(List.of("keyword", "limit", "count", "ratio", "enabled"), schema.get("required"));
        assertEquals("object", schema.get("type"));
    }

    @Test
    void optionalParam() throws Exception {
        Map<String, Object> schema = gen.paramSchema(
                Controller.class.getMethod("optional", String.class, Optional.class));
        @SuppressWarnings("unchecked")
        Map<String, Object> props = (Map<String, Object>) schema.get("properties");
        assertEquals(List.of("keyword"), schema.get("required"));
        @SuppressWarnings("unchecked")
        Map<String, Object> tag = (Map<String, Object>) props.get("tag");
        assertEquals(List.of("string", "null"), tag.get("type")); // Optional → 可空
    }

    @Test
    void containers() throws Exception {
        Map<String, Object> schema = gen.paramSchema(
                Controller.class.getMethod("containers", List.class, Map.class));
        @SuppressWarnings("unchecked")
        Map<String, Object> props = (Map<String, Object>) schema.get("properties");
        Map<?, ?> items = (Map<?, ?>) props.get("items");
        assertEquals("array", items.get("type"));
        assertEquals("string", ((Map<?, ?>) items.get("items")).get("type"));
        Map<?, ?> counts = (Map<?, ?>) props.get("counts");
        assertEquals("object", counts.get("type"));
        assertEquals("integer", ((Map<?, ?>) counts.get("additionalProperties")).get("type"));
    }

    @Test
    void nestedPojo() throws Exception {
        Map<String, Object> schema = gen.paramSchema(
                Controller.class.getMethod("pojo", Song.class));
        @SuppressWarnings("unchecked")
        Map<String, Object> props = (Map<String, Object>) schema.get("properties");
        @SuppressWarnings("unchecked")
        Map<String, Object> song = (Map<String, Object>) props.get("song");
        assertEquals("object", song.get("type"));
        @SuppressWarnings("unchecked")
        Map<String, Object> songProps = (Map<String, Object>) song.get("properties");
        assertTrue(songProps.containsKey("id"));
        assertTrue(songProps.containsKey("title"));
        assertEquals("integer", ((Map<?, ?>) songProps.get("duration")).get("type"));
    }

    @Test
    void annotationDescription() throws Exception {
        class Local {
            @SuppressWarnings("unused")
            public void search(@AgentParam(description = "搜索关键词") String keyword) {
            }
        }
        Map<String, Object> schema = gen.paramSchema(Local.class.getMethod("search", String.class));
        @SuppressWarnings("unchecked")
        Map<String, Object> props = (Map<String, Object>) schema.get("properties");
        @SuppressWarnings("unchecked")
        Map<String, Object> kw = (Map<String, Object>) props.get("keyword");
        assertEquals("搜索关键词", kw.get("description"));
    }

    @Test
    void noRequiredWhenOptionalAnnotated() throws Exception {
        class Local {
            @SuppressWarnings("unused")
            public void f(@AgentParam(required = false) String tag) {
            }
        }
        Map<String, Object> schema = gen.paramSchema(Local.class.getMethod("f", String.class));
        assertFalse(schema.containsKey("required"));
    }
}
