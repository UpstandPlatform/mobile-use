package org.mobileuse.control;

import android.app.Activity;
import android.app.Instrumentation;
import android.app.UiAutomation;
import android.os.Bundle;
import android.os.SystemClock;
import android.util.Base64;
import android.view.InputDevice;
import android.view.MotionEvent;
import android.view.accessibility.AccessibilityNodeInfo;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.nio.charset.StandardCharsets;
import java.util.ArrayDeque;

public final class ControlInstrumentation extends Instrumentation {
    private static final int BASE64_FLAGS = Base64.URL_SAFE | Base64.NO_WRAP | Base64.NO_PADDING;
    private String encoded;

    @Override public void onCreate(Bundle arguments) {
        super.onCreate(arguments);
        encoded = arguments.getString("request", "");
        start();
    }

    @Override public void onStart() {
        String result = "unsupported";
        try {
            if (encoded.length() > 12000) throw new IllegalArgumentException("Oversized input");
            JSONObject request = new JSONObject(new String(Base64.decode(encoded, BASE64_FLAGS), StandardCharsets.UTF_8));
            UiAutomation automation = getUiAutomation(UiAutomation.FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES);
            result = switch (request.getString("type")) {
                case "capabilities" -> "touch_paths_v1";
                case "type" -> type(automation, request.getString("text"), request.getString("app_id"));
                case "touches" -> touches(automation, request);
                case "pinch" -> pinch(automation, request);
                default -> "unsupported";
            };
        } catch (IllegalArgumentException | JSONException | SecurityException ignored) {
            // The connector reports these as unavailable without emitting input text.
        } catch (RuntimeException ignored) {
            result = "unknown";
        }
        Bundle response = new Bundle();
        response.putString("mobile_use_result", result);
        finish(Activity.RESULT_OK, response);
    }

    private static String type(UiAutomation automation, String text, String appId) {
        if (text.isEmpty() || text.length() > 4096) return "unsupported";
        AccessibilityNodeInfo focused = null;
        for (int attempt = 0; attempt < 4 && focused == null; attempt++) {
            focused = focusedEditor(automation, appId);
            if (focused == null) SystemClock.sleep(60);
        }
        if (focused == null) return "no_editor";
        if (focused.isPassword()) return "protected";
        CharSequence existing = focused.getText();
        String original = existing == null || focused.isShowingHintText() ? "" : existing.toString();
        if (original.length() > 16384) return "unsupported";
        int start = focused.getTextSelectionStart();
        int end = focused.getTextSelectionEnd();
        if (start < 0 || end < start || end > original.length()) start = end = original.length();
        String prefix = original.substring(0, start) + text;
        Bundle replacement = new Bundle();
        replacement.putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, prefix + original.substring(end));
        if (!focused.performAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_SET_TEXT.getId(), replacement)) return "text_rejected";
        Bundle selection = new Bundle();
        selection.putInt(AccessibilityNodeInfo.ACTION_ARGUMENT_SELECTION_START_INT, prefix.length());
        selection.putInt(AccessibilityNodeInfo.ACTION_ARGUMENT_SELECTION_END_INT, prefix.length());
        focused.performAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_SET_SELECTION.getId(), selection);
        return "ok";
    }

    private static AccessibilityNodeInfo focusedEditor(UiAutomation automation, String appId) {
        AccessibilityNodeInfo direct = automation.findFocus(AccessibilityNodeInfo.FOCUS_INPUT);
        if (editor(direct, appId)) return direct;

        AccessibilityNodeInfo root = automation.getRootInActiveWindow();
        if (root == null) return null;
        ArrayDeque<AccessibilityNodeInfo> remaining = new ArrayDeque<>();
        remaining.add(root);
        AccessibilityNodeInfo focused = null;
        int visited = 0;
        while (!remaining.isEmpty() && visited++ < 1000) {
            AccessibilityNodeInfo node = remaining.removeFirst();
            if (editor(node, appId) && node.isFocused()) {
                if (focused != null) return null;
                focused = node;
            }
            for (int i = 0; i < node.getChildCount() && remaining.size() < 1000; i++) {
                AccessibilityNodeInfo child = node.getChild(i);
                if (child != null) remaining.addLast(child);
            }
        }
        return focused;
    }

    private static boolean editor(AccessibilityNodeInfo node, String appId) {
        return node != null && appId.contentEquals(node.getPackageName() == null ? "" : node.getPackageName())
            && node.isEditable() && node.isEnabled() && node.isVisibleToUser();
    }

    private static String pinch(UiAutomation automation, JSONObject request) throws JSONException {
        float[][] start = positions(request.getJSONArray("starts"));
        float[][] end = positions(request.getJSONArray("ends"));
        int duration = Math.min(3000, Math.max(100, request.optInt("duration_ms", 350)));
        long down = SystemClock.uptimeMillis();
        if (!event(automation, down, MotionEvent.ACTION_DOWN, 1, start)) return "unknown";
        boolean finished = false;
        try {
            if (!event(automation, down, MotionEvent.ACTION_POINTER_DOWN | (1 << MotionEvent.ACTION_POINTER_INDEX_SHIFT), 2, start)) return "unknown";
            float[][] intermediate = new float[2][2];
            for (int elapsed = 0; elapsed < duration; elapsed += 16) {
                float fraction = (float) elapsed / duration;
                for (int finger = 0; finger < 2; finger++) {
                    for (int axis = 0; axis < 2; axis++) intermediate[finger][axis] = start[finger][axis] + fraction * (end[finger][axis] - start[finger][axis]);
                }
                if (!event(automation, down, MotionEvent.ACTION_MOVE, 2, intermediate)) return "unknown";
                SystemClock.sleep(16);
            }
            if (!event(automation, down, MotionEvent.ACTION_MOVE, 2, end)) return "unknown";
            if (!event(automation, down, MotionEvent.ACTION_POINTER_UP | (1 << MotionEvent.ACTION_POINTER_INDEX_SHIFT), 2, end)) return "unknown";
            finished = event(automation, down, MotionEvent.ACTION_UP, 1, end);
            return finished ? "ok" : "unknown";
        } finally {
            if (!finished) event(automation, down, MotionEvent.ACTION_CANCEL, 2, end);
        }
    }

    private static String touches(UiAutomation automation, JSONObject request) throws JSONException {
        JSONArray paths = request.getJSONArray("paths");
        int count = paths.length();
        if (count < 2 || count > 5) return "unsupported";
        float[][] starts = new float[count][2];
        float[][] ends = new float[count][2];
        for (int finger = 0; finger < count; finger++) {
            JSONArray path = paths.getJSONArray(finger);
            if (path.length() != 2) throw new IllegalArgumentException("Invalid touch path");
            for (int axis = 0; axis < 2; axis++) {
                double start = path.getJSONArray(0).getDouble(axis);
                double end = path.getJSONArray(1).getDouble(axis);
                if (!Double.isFinite(start) || !Double.isFinite(end) || start < 0 || end < 0 || start > 10000 || end > 10000) {
                    throw new IllegalArgumentException("Invalid touch coordinates");
                }
                starts[finger][axis] = (float) start;
                ends[finger][axis] = (float) end;
            }
        }
        int duration = Math.min(3000, Math.max(80, request.optInt("duration_ms", 350)));
        long down = SystemClock.uptimeMillis();
        int activePointers = 0;
        boolean finished = false;
        try {
            if (!event(automation, down, MotionEvent.ACTION_DOWN, 1, starts)) return "unknown";
            activePointers = 1;
            for (int finger = 1; finger < count; finger++) {
                int action = MotionEvent.ACTION_POINTER_DOWN | (finger << MotionEvent.ACTION_POINTER_INDEX_SHIFT);
                if (!event(automation, down, action, finger + 1, starts)) return "unknown";
                activePointers++;
            }
            for (int elapsed = 0; elapsed < duration; elapsed += 16) {
                float fraction = (float) elapsed / duration;
                float[][] intermediate = interpolate(starts, ends, fraction);
                if (!event(automation, down, MotionEvent.ACTION_MOVE, activePointers, intermediate)) return "unknown";
                SystemClock.sleep(16);
            }
            if (!event(automation, down, MotionEvent.ACTION_MOVE, activePointers, ends)) return "unknown";
            for (int finger = count - 1; finger > 0; finger--) {
                int action = MotionEvent.ACTION_POINTER_UP | (finger << MotionEvent.ACTION_POINTER_INDEX_SHIFT);
                if (!event(automation, down, action, activePointers, ends)) return "unknown";
                activePointers--;
            }
            finished = event(automation, down, MotionEvent.ACTION_UP, 1, ends);
            return finished ? "ok" : "unknown";
        } finally {
            if (!finished && activePointers > 0) event(automation, down, MotionEvent.ACTION_CANCEL, activePointers, ends);
        }
    }

    private static float[][] interpolate(float[][] starts, float[][] ends, float fraction) {
        float[][] result = new float[starts.length][2];
        for (int finger = 0; finger < starts.length; finger++) {
            for (int axis = 0; axis < 2; axis++) {
                result[finger][axis] = starts[finger][axis] + fraction * (ends[finger][axis] - starts[finger][axis]);
            }
        }
        return result;
    }

    private static float[][] positions(JSONArray values) throws JSONException {
        if (values.length() != 2) throw new IllegalArgumentException("Expected two fingers");
        float[][] result = new float[2][2];
        for (int i = 0; i < 2; i++) {
            JSONArray point = values.getJSONArray(i);
            if (point.length() != 2) throw new IllegalArgumentException("Invalid coordinates");
            for (int axis = 0; axis < 2; axis++) {
                double value = point.getDouble(axis);
                if (!Double.isFinite(value) || value < 0 || value > 10000) throw new IllegalArgumentException("Invalid coordinates");
                result[i][axis] = (float) value;
            }
        }
        return result;
    }

    private static boolean event(UiAutomation automation, long down, int action, int count, float[][] points) {
        MotionEvent.PointerProperties[] properties = new MotionEvent.PointerProperties[count];
        MotionEvent.PointerCoords[] coordinates = new MotionEvent.PointerCoords[count];
        for (int index = 0; index < count; index++) {
            properties[index] = new MotionEvent.PointerProperties();
            properties[index].id = index;
            properties[index].toolType = MotionEvent.TOOL_TYPE_FINGER;
            coordinates[index] = new MotionEvent.PointerCoords();
            coordinates[index].x = points[index][0];
            coordinates[index].y = points[index][1];
            coordinates[index].pressure = 1;
            coordinates[index].size = 1;
        }
        MotionEvent event = MotionEvent.obtain(down, SystemClock.uptimeMillis(), action, count, properties, coordinates,
            0, 0, 1, 1, 0, 0, InputDevice.SOURCE_TOUCHSCREEN, 0);
        try { return automation.injectInputEvent(event, true); }
        finally { event.recycle(); }
    }
}
